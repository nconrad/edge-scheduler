package nodescheduler

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
	"time"

	uuid "github.com/nu7hatch/gouuid"
	"github.com/sagecontinuum/ses/pkg/datatype"
	"github.com/sagecontinuum/ses/pkg/interfacing"
	"github.com/sagecontinuum/ses/pkg/logger"
	yaml "gopkg.in/yaml.v2"
	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// GoalManager structs a goal manager for nodescheduler
type NodeGoalManager struct {
	ScienceGoals             map[string]*datatype.ScienceGoal
	cloudSchedulerBaseURL    string
	NodeID                   string
	Simulate                 bool
	chanNewGoalToGoalManager chan *datatype.ScienceGoal
	rmqHandler               *interfacing.RabbitMQHandler
	GoalWatcher              watch.Interface
}

// NewGoalManager creates and returns an instance of goal manager
func NewNodeGoalManager(cloudSchedulerURL string, nodeID string, simulate bool) (*NodeGoalManager, error) {
	return &NodeGoalManager{
		ScienceGoals:             make(map[string]*datatype.ScienceGoal),
		cloudSchedulerBaseURL:    cloudSchedulerURL,
		NodeID:                   nodeID,
		Simulate:                 simulate,
		chanNewGoalToGoalManager: make(chan *datatype.ScienceGoal, 100),
	}, nil
}

// GetScienceGoal returns the goal of given goal_id
func (ngm *NodeGoalManager) GetScienceGoal(goalID string) (*datatype.ScienceGoal, error) {
	if goal, exist := ngm.ScienceGoals[goalID]; exist {
		return goal, nil
	}
	return nil, fmt.Errorf("The goal %s does not exist", goalID)
}

// SetRMQHandler sets a RabbitMQ handler used for transferring goals to edge schedulers
func (ngm *NodeGoalManager) SetRMQHandler(rmqHandler *interfacing.RabbitMQHandler) {
	ngm.rmqHandler = rmqHandler
}

// RunGoalManager handles goal related events from both cloud and local
// and keeps goals managed up-to-date with the help from the events
func (ngm *NodeGoalManager) Run(chanToScheduler chan datatype.Event) {
	// NOTE: use RabbitMQ to receive goals if set
	// var useRabbitMQ bool
	// if ngm.rmqHandler != nil {
	// 	useRabbitMQ = true
	// } else {
	// 	useRabbitMQ = false
	// }
	if !ngm.Simulate {
		// go ngm.pullGoalsFromCloudScheduler(useRabbitMQ)
		go ngm.pullGoalsFromK3S()
	}
	for {
		select {
		case scienceGoal := <-ngm.chanNewGoalToGoalManager:
			logger.Info.Printf("Received a goal %q", scienceGoal.Name)
			ngm.ScienceGoals[scienceGoal.Name] = scienceGoal
			chanToScheduler <- datatype.NewEvent(datatype.EventNewGoal, scienceGoal.Name)
		}
	}
}

func (ngm *NodeGoalManager) pullGoalsFromK3S() {
	logger.Info.Printf("Pull goals from k3s configmap %s", configMapNameForGoals)
	if ngm.GoalWatcher == nil {
		logger.Error.Printf("No Goal watcher is set. Cannot pull goals from k3s configmap %s", configMapNameForGoals)
		return
	}
	chanGoal := ngm.GoalWatcher.ResultChan()
	for {
		event := <-chanGoal
		switch event.Type {
		case watch.Added:
			fallthrough
		case watch.Modified:
			if updatedConfigMap, ok := event.Object.(*apiv1.ConfigMap); ok {
				logger.Debug.Printf("%v", updatedConfigMap.Data)
				var jobTemplate datatype.JobTemplate
				err := yaml.Unmarshal([]byte(updatedConfigMap.Data["goals"]), &jobTemplate)
				if err != nil {
					logger.Error.Printf("Failed to load goals from Kubernetes ConfigMap %q", err.Error())
				} else {
					scienceGoal, err := ngm.convertJobTemplateToScienceGoal(&jobTemplate)
					if err != nil {
						logger.Error.Printf("Failed to convert into Science Goal %q", err.Error())
					} else {
						ngm.chanNewGoalToGoalManager <- scienceGoal
					}
				}
			}
		}
	}
}

func (gm *NodeGoalManager) convertJobTemplateToScienceGoal(job *datatype.JobTemplate) (*datatype.ScienceGoal, error) {
	u, _ := uuid.NewV4()
	var subGoal datatype.SubGoal
	subGoal.Node = &datatype.Node{
		Name: gm.NodeID,
	}
	for _, pluginSpec := range job.Plugins {
		subGoal.Plugins = append(subGoal.Plugins, &datatype.Plugin{
			Name:       pluginSpec.Name,
			PluginSpec: pluginSpec,
			Status: datatype.PluginStatus{
				SchedulingStatus: datatype.Waiting,
			},
		})
	}
	return &datatype.ScienceGoal{
		ID:   u.String(),
		Name: job.Name,
		SubGoals: []*datatype.SubGoal{
			&subGoal,
		},
	}, nil
}

// pullingGoalsFromCloudScheduler periodically pulls goals from the cloud scheduler
func (gm *NodeGoalManager) pullGoalsFromCloudScheduler(useRabbitMQ bool) {
	if useRabbitMQ {
		for {
			logger.Info.Printf("SES endpoint: %s", gm.rmqHandler.RabbitmqURI)
			msgs, err := gm.rmqHandler.GetReceiver(gm.NodeID)
			if err != nil {
				logger.Error.Printf("%s", err)
				time.Sleep(5 * time.Second)
				continue
			}
			for d := range msgs {
				var pulledGoals []datatype.ScienceGoal
				err = yaml.Unmarshal(d.Body, &pulledGoals)
				if err != nil {
					logger.Error.Printf("%s", err)
				}
				logger.Info.Printf("%v", pulledGoals)
				// TODO: this does not account for goal status change
				//       from SES -- may or may not happen
				// for _, goal := range pulledGoals {
				// 	if _, exist := gm.scienceGoals[goal.ID]; !exist {
				// 		gm.chanNewGoalToGoalManager <- &goal
				// 	}
				// }
			}
		}
	} else {
		queryURL, _ := url.Parse(gm.cloudSchedulerBaseURL)
		queryURL.Path = path.Join(queryURL.Path, "api/v1/goals/", gm.NodeID)
		logger.Info.Printf("SES endpoint: %s", queryURL.String())
		for {
			time.Sleep(5 * time.Second)
			resp, err := http.Get(queryURL.String())
			if err != nil {
				logger.Error.Printf("%s", err)
				continue
			}
			defer resp.Body.Close()

			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				logger.Error.Printf("%s", err)
				continue
			}
			var pulledGoals []datatype.ScienceGoal
			_ = yaml.Unmarshal(body, &pulledGoals)

			// TODO: this does not account for goal status change
			//       from SES -- may or may not happen
			// for _, goal := range pulledGoals {
			// 	if _, exist := gm.scienceGoals[goal.ID]; !exist {
			// 		gm.chanNewGoalToGoalManager <- &goal
			// 	}
			// }
		}
	}
}
