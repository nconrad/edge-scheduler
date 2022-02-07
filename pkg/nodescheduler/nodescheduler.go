package nodescheduler

import (
	"github.com/sagecontinuum/ses/pkg/datatype"
	"github.com/sagecontinuum/ses/pkg/interfacing"
	"github.com/sagecontinuum/ses/pkg/knowledgebase"
	"github.com/sagecontinuum/ses/pkg/logger"
	"github.com/sagecontinuum/ses/pkg/nodescheduler/policy"
)

const (
	maxChannelBuffer      = 100
	configMapNameForGoals = "waggle-plugin-scheduler-goals"
)

type NodeScheduler struct {
	NodeID                      string
	ResourceManager             *ResourceManager
	Knowledgebase               *knowledgebase.Knowledgebase
	GoalManager                 *NodeGoalManager
	APIServer                   *APIServer
	Simulate                    bool
	SchedulingPolicy            *policy.SchedulingPolicy
	LogToBeehive                *interfacing.RabbitMQHandler
	chanContextEventToScheduler chan datatype.EventPluginContext
	chanFromGoalManager         chan datatype.Event
	chanFromResourceManager     chan datatype.Event
	chanRunGoal                 chan *datatype.ScienceGoal
	chanStopPlugin              chan *datatype.Plugin
	chanPluginToResourceManager chan *datatype.Plugin
	chanNeedScheduling          chan datatype.Event
	chanAPIServerToGoalManager  chan *datatype.ScienceGoal
}

// func NewNodeScheduler(simulate bool) &NodeScheduler {
// 	// schedulingPolicy := policy.NewSimpleSchedulingPolicy()
// 	return &NodeScheduler{
// 		Simulate:                    simulate,
// 		SchedulingPolicy:            schedulingPolicy,
// 		chanContextEventToScheduler: make(chan datatype.EventPluginContext, maxChannelBuffer),
// 		chanFromGoalManager:         make(chan datatype.Event, maxChannelBuffer),
// 		chanRunGoal:                 make(chan *datatype.ScienceGoal, maxChannelBuffer),
// 		chanStopPlugin:              make(chan *datatype.Plugin, maxChannelBuffer),
// 		chanPluginToResourceManager: make(chan *datatype.Plugin, maxChannelBuffer),
// 		chanNeedScheduling:          make(chan string, 1),
// 		chanAPIServerToGoalManager:  make(chan *datatype.ScienceGoal, maxChannelBuffer),
// 	}
// }

// Configure sets up the followings in Kubernetes cluster
//
// - "ses" namespace
//
// - "wes-rabbitmq" and "wes-audio-server" services available in "ses" namespace
//
// - "waggle-data-config" and "wes-audio-server-plugin-conf" configmaps
//
// - "wes-ses-goal" configmap that accepts user goals
func (ns *NodeScheduler) Configure() (err error) {
	if ns.Simulate {
		return
	}
	err = ns.ResourceManager.CreateNamespace("ses")
	if err != nil {
		return
	}
	servicesToBringUp := []string{"wes-rabbitmq", "wes-audio-server"}
	for _, service := range servicesToBringUp {
		err = ns.ResourceManager.ForwardService(service, "default", "ses")
		if err != nil {
			return
		}
	}
	configMapsToBring := []string{"waggle-data-config", "wes-audio-server-plugin-conf"}
	for _, configMapName := range configMapsToBring {
		err = ns.ResourceManager.CopyConfigMap(configMapName, "default", ns.ResourceManager.Namespace)
		if err != nil {
			logger.Error.Printf("Failed to create ConfigMap %q: %q", configMapName, err.Error())
		}
	}
	err = ns.ResourceManager.CreateConfigMap(configMapNameForGoals, map[string]string{}, "default", false)
	if err != nil {
		return
	}
	watcher, err := ns.ResourceManager.WatchConfigMap(configMapNameForGoals, "default")
	if err != nil {
		return
	}
	ns.GoalManager.GoalWatcher = watcher
	return nil
}

// Run handles communications between components for scheduling
func (ns *NodeScheduler) Run() {
	go ns.GoalManager.Run(ns.chanFromGoalManager)
	// go ns.Knowledgebase.Run(ns.chanContextEventToScheduler)
	go ns.ResourceManager.Run(ns.chanPluginToResourceManager)
	// NOTE: The garbage collector runs to clean up completed/failed jobs
	//       This should be done by Kubernetes with versions higher than v1.21
	//       v1.20 could do it by enabling TTL controller, but could not set it
	//       via k3s server --kube-control-manager-arg feature-gates=TTL...=true
	go ns.ResourceManager.RunGabageCollector()
	go ns.APIServer.Run()
	var currentGoalID string = ""
	for {
		select {
		// case contextEvent := <-ns.chanContextEventToScheduler:
		// 	scienceGoal, err := ns.GoalManager.GetScienceGoal(contextEvent.GoalID)
		// 	if err != nil {
		// 		logger.Error.Printf("%s", err.Error())
		// 		continue
		// 	}
		// 	subGoal := scienceGoal.GetMySubGoal(ns.GoalManager.NodeID)
		// 	err = subGoal.UpdatePluginContext(contextEvent)
		// 	if err != nil {
		// 		logger.Error.Printf("%s", err.Error())
		// 		continue
		// 	}
		// 	// When a plugin becomes runnable see if it can be scheduled
		// 	if contextEvent.Status == datatype.Runnable {
		// 		ns.chanRunGoal <- scienceGoal
		// 	} else if contextEvent.Status == datatype.Stoppable {
		// 		ns.chanStopPlugin <- subGoal.GetPlugin(contextEvent.PluginName)
		// 	}
		case event := <-ns.chanFromGoalManager:
			// ns.Knowledgebase.RegisterRules(scienceGoal, ns.GoalManager.NodeID)
			logger.Debug.Printf("Event: %q received with meta %q", event.Type, event.Body)
			ns.ResourceManager.CleanUp()
			currentGoalID = event.Body
			ns.chanNeedScheduling <- event
		case event := <-ns.chanFromResourceManager:
			logger.Debug.Printf("Event: %q received with meta %q", event.Type, event.Body)
			pluginName := event.Body
			switch event.Type {
			case datatype.EventPluginStatusLaunched:
				scienceGoal, err := ns.GoalManager.GetScienceGoal(currentGoalID)
				if err != nil {
					logger.Error.Printf("Could not get goal to update plugin status: %q", err.Error())
				} else {
					plugin := scienceGoal.GetMySubGoal(ns.NodeID).GetPlugin(pluginName)
					if plugin != nil {
						plugin.UpdatePluginSchedulingStatus(datatype.Running)
						go ns.LogToBeehive.SendWaggleMessage(event.ToWaggleMessage(), "all")
					}
				}
			case datatype.EventPluginStatusComplete, datatype.EventPluginStatusFailed:
				scienceGoal, err := ns.GoalManager.GetScienceGoal(currentGoalID)
				if err != nil {
					logger.Error.Printf("Could not get goal to update plugin status: %q", err.Error())
				} else {
					plugin := scienceGoal.GetMySubGoal(ns.NodeID).GetPlugin(pluginName)
					if plugin != nil {
						plugin.UpdatePluginSchedulingStatus(datatype.Waiting)
						go ns.LogToBeehive.SendWaggleMessage(event.ToWaggleMessage(), "all")
					}
				}
			}
			ns.chanNeedScheduling <- event
		// case scheduledScienceGoal := <-ns.chanRunGoal:
		// 	logger.Info.Printf("Goal %s needs scheduling", scheduledScienceGoal.Name)
		// 	subGoal := scheduledScienceGoal.GetMySubGoal(ns.GoalManager.NodeID)
		// 	pluginsSubjectToSchedule := subGoal.GetSchedulablePlugins()
		// 	logger.Info.Printf("Plugins subject to run: %v", pluginsSubjectToSchedule)
		// 	// TODO: Resource model is not applied here -- needs improvements
		// 	orderedPluginsToRun := policy.SimpleSchedulingPolicy(pluginsSubjectToSchedule, datatype.Resource{
		// 		CPU:       999999,
		// 		Memory:    999999,
		// 		GPUMemory: 999999,
		// 	})
		// 	logger.Debug.Printf("Ordered plugins subject to run: %v", orderedPluginsToRun)
		// 	// Launch plugins
		// 	for _, plugin := range orderedPluginsToRun {
		// 		plugin.Status.SchedulingStatus = datatype.Running
		// 		ns.chanPluginToResourceManager <- plugin
		// 		logger.Info.Printf("Plugin %s has been scheduled to run", plugin.Name)
		// 	}
		// 	// // Launch plugins
		// 	// if launchPlugins(schedulablePluginConfigs, pluginsToRun) {
		// 	// 	// Track the plugin
		// 	// 	// TODO: Later get status from k3s to track running plugins
		// 	// 	currentPlugins = append(currentPlugins, pluginsToRun...)
		// 	// }
		// 	// logger.Info.Print("======================================")
		// 	// scheduleTriggered = false
		// case pluginToStop := <-ns.chanStopPlugin:
		// 	if pluginToStop.Status.SchedulingStatus == datatype.Running {
		// 		pluginToStop.Status.SchedulingStatus = datatype.Stopped
		// 		ns.chanPluginToK3SClient <- pluginToStop
		// 		logger.Info.Printf("Plugin %s has been triggered to stop", pluginToStop.Name)
		// 	}
		case event := <-ns.chanNeedScheduling:
			logger.Debug.Printf("Reason for (re)scheduling %q", event.Type)
			// Main logic: round robin + FIFO
			// Promote any waiting plugins
			for name, goal := range ns.GoalManager.ScienceGoals {
				logger.Debug.Printf("Checking Goal %q", name)
				subGoal := goal.GetMySubGoal(ns.GoalManager.NodeID)
				ns.SchedulingPolicy.SimpleScheduler.PromotePlugins(subGoal)
			}
			// Selecte best task
			plugin, err := ns.SchedulingPolicy.SimpleScheduler.SelectBestTask(
				ns.GoalManager.ScienceGoals,
				datatype.Resource{
					CPU:       999999,
					Memory:    999999,
					GPUMemory: 999999,
				},
				ns.GoalManager.NodeID,
			)
			if err != nil {
				logger.Error.Printf("Failed to get the best task to run %q", err.Error())
			} else {
				if plugin != nil {
					if ns.ResourceManager.WillItFit(plugin) {
						logger.Debug.Printf("Send %q to Resource Manager", plugin.Name)
						plugin.UpdatePluginSchedulingStatus(datatype.Ready)
						go ns.ResourceManager.LaunchAndWatchPlugin(plugin)
					} else {
						logger.Debug.Printf("Resource is not availble for plugin %q", plugin.Name)
					}
				}
			}
		}
	}
}
