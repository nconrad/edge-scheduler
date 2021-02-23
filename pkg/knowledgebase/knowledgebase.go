package knowledgebase

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sagecontinuum/ses/pkg/datatype"
	"github.com/sagecontinuum/ses/pkg/logger"
	"github.com/zeromq/goczmq"
)

const (
	maxChannelBuffer = 100
)

// RequestToKB structs a message for KB
type RequestToKB struct {
	ReturnCode int         `json:"return_code"`
	Command    string      `json:"command"`
	Args       []string    `json:"args"`
	Result     interface{} `json:"result"`
}

// Knowledgebase structs an instance of knowledgebase
type Knowledgebase struct {
	pathToPythonKB string
	chanToKB       chan RequestToKB
}

// NewKnowledgebase creates and returns an instance of Knowledgebase
func NewKnowledgebase() (kb *Knowledgebase, err error) {
	kb = &Knowledgebase{
		pathToPythonKB: "kb.py",
		chanToKB:       make(chan RequestToKB, maxChannelBuffer),
	}
	return
}

// Run runs the logic of Knowledgebase
func (kb *Knowledgebase) Run(chanContextEventToScheduler chan<- datatype.EventPluginContext) {
	go kb.launchKB()
	go kb.runIPCToKB()

	for {
		chanExit := make(chan error)
		go func() {
			logger.Info.Printf("(re)Starting event listener...")
			socket, err := goczmq.NewPair("ipc:///tmp/event.sock")
			// socket = socket.Connect("ipc:///tmp/event.sock")
			if err != nil {
				chanExit <- err
				return
			}
			defer socket.Destroy()
			for {
				byteMessage, _, err := socket.RecvFrame()
				if err != nil {
					chanExit <- err
					return
				}
				var event datatype.EventPluginContext
				err = json.Unmarshal(byteMessage, &event)
				if err != nil {
					logger.Error.Printf("Failed to parse plugin context event %s", byteMessage)
					continue
				}
				// scheduler (especially k3s) does not like Cap words...
				event.PluginName = strings.ToLower(event.PluginName)
				chanContextEventToScheduler <- event
				logger.Info.Printf("Event received: %v", event)
			}
		}()
		err := <-chanExit
		logger.Error.Printf("Event receiver failed: %s", err)
		time.Sleep(3 * time.Second)
	}
}

// RegisterRules registers rules of a goal to the KB engine
func (kb *Knowledgebase) RegisterRules(scienceGoal *datatype.ScienceGoal, nodeName string) {
	mySubGoal := scienceGoal.GetMySubGoal(nodeName)

	logger.Info.Printf("Loading science rules to KB...")
	rules := []string{scienceGoal.ID}
	rules = append(rules, mySubGoal.Sciencerules...)
	kb.chanToKB <- RequestToKB{
		Command: "rule",
		Args:    rules,
	}
}

// runIPCToKB communicates with the Python KB to exchange rules and events
func (kb *Knowledgebase) runIPCToKB() {
	for {
		chanExit := make(chan error)
		go func() {
			socket, err := goczmq.NewReq("ipc:///tmp/kb.sock")
			if err != nil {
				chanExit <- err
				return
			}
			defer socket.Destroy()
			for {
				request := <-kb.chanToKB
				byteJSON, _ := json.Marshal(request)
				err = socket.SendFrame(byteJSON, goczmq.FlagNone)
				if err != nil {
					chanExit <- err
					return
				}
				_, _, err = socket.RecvFrame()
				if err != nil {
					chanExit <- err
					return
				} else {
					logger.Info.Printf("%v is sent to KB", request)
				}
			}
		}()
		err := <-chanExit
		logger.Error.Printf("IPC to KB failed: %s", err)
		time.Sleep(3 * time.Second)
	}
}

// launchKB launches and manages the Python KB
func (kb *Knowledgebase) launchKB() {
	args := []string{kb.pathToPythonKB}
	for {
		logger.Info.Printf("Launching KB...")
		cmd := exec.Command("python3", args...)
		// TODO: Making sure cmd does not hang after terminating the parent process
		//       This may help https://bigkevmcd.github.io/go/pgrp/context/2019/02/19/terminating-processes-in-go.html
		cmd.Env = append(os.Environ(),
			"WAGGLE_PLUGIN_HOST=10.31.81.10",
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			logger.Info.Printf("kb.py failed with %s", err.Error())
		}
		logger.Info.Printf("Restarting kb.py in 5 seconds...")
		time.Sleep(5 * time.Second)
	}
}
