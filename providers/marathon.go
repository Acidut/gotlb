package providers

import (
	"fmt"
	"log"
	"net/url"

	"github.com/ashwanthkumar/golang-utils/maps"
	"github.com/ashwanthkumar/gotlb/types"
	marathon "github.com/gambol99/go-marathon"
)

type Labels map[string]string

type MarathonProvider struct {
	addBackend    chan<- *types.BackendInfo
	removeBackend chan<- *types.BackendInfo
	appUpdate     chan<- *types.AppInfo
	dropApp       chan<- *types.AppInfo
	stopMe        <-chan bool
	apps          map[string]Labels

	marathonHost string
}

// NewMarathonProvider creates a new marathon based provider for GoTLB to discover
// new backends for the TCP server dynamically directly from Marathon's Event bus
func NewMarathonProvider(marathonHost string) Provider {
	return &MarathonProvider{
		marathonHost: marathonHost,
		apps:         make(map[string]Labels),
	}
}

func (m *MarathonProvider) Provide(
	addBackend chan<- *types.BackendInfo,
	removeBackend chan<- *types.BackendInfo,
	appUpdate chan<- *types.AppInfo,
	dropApp chan<- *types.AppInfo,
	stop <-chan bool) error {
	m.addBackend = addBackend
	m.removeBackend = removeBackend
	m.appUpdate = appUpdate
	m.dropApp = dropApp
	m.stopMe = stop
	log.Println("Starting Marathon Provider on " + m.marathonHost)
	go m.start()
	log.Println("Marathon Provider Started and configured to " + m.marathonHost)
	return nil
}

func (m *MarathonProvider) start() {
	config := marathon.NewDefaultConfig()
	config.URL = m.marathonHost
	config.EventsTransport = marathon.EventsTransportSSE
	client, err := marathon.NewClient(config)
	if err != nil {
		log.Fatalf("Unable to create marathon client - %v\n", err)
	}

	// Scan through all the apps on starting up
	m.scanAllApps(client)

	eventsChannel, err := client.AddEventsListener(marathon.EventIDAPIRequest | marathon.EventIDStatusUpdate | marathon.EventIDFailedHealthCheck | marathon.EventIDAppTerminated)
	if err != nil {
		log.Fatalf("Unable to create events listener - %v\n", err)
	}

	running := true
	for running {
		select {
		case event := <-eventsChannel:
			switch event.ID {
			case marathon.EventIDStatusUpdate:
				update := event.Event.(*marathon.EventStatusUpdate)
				// check if the update is for known app
				knownApp := m.containsApp(update.AppID)

				if knownApp && update.TaskStatus == "TASK_FAILED" {
					m.removeBackend <- m.createBackendInfo(update.AppID, update.IPAddresses, update.Ports)
				} else if knownApp && update.TaskStatus == "TASK_RUNNING" {
					m.addBackend <- m.createBackendInfo(update.AppID, update.IPAddresses, update.Ports)
				}
				// fmt.Printf("app=%s, id=%s, slaveId=%s, status=%s, host:ip=%s:%d\n", update.AppID, update.TaskID, update.SlaveID, update.TaskStatus, update.IPAddresses[0].IPAddress, update.Ports[0])
			case marathon.EventIDAPIRequest:
				app := event.Event.(*marathon.EventAPIRequest)
				_, err := client.Application(app.AppDefinition.ID)
				if err != nil {
					log.Printf("[WARN] Unable to get application - %s - %v\n", app.AppDefinition.ID, err)
					fmt.Printf("Deleted the App spec - %v\n", app)
					// check if the update is for known app, only then propagate
					knownApp := m.containsApp(app.AppDefinition.ID)
					if knownApp {
						// most likely the app was destroyed
						m.dropApp <- &types.AppInfo{
							AppId:  app.AppDefinition.ID,
							Labels: *app.AppDefinition.Labels,
						}
					}
				} else {
					fmt.Printf("New / Updated the App spec - %v\n", app)
					m.appUpdate <- &types.AppInfo{
						AppId:  app.AppDefinition.ID,
						Labels: *app.AppDefinition.Labels,
					}
				}
			}
		case <-m.stopMe:
			running = false
			client.RemoveEventsListener(eventsChannel)
		}
	}
}

func (m *MarathonProvider) scanAllApps(client marathon.Marathon) {
	v := url.Values{}
	v.Set("embed", "apps.tasks")
	apps, err := client.Applications(v)
	if err != nil {
		log.Printf("[WARN] Initializing with all applications failed - %v\n", err)
	} else {
		for _, app := range apps.Apps {
			if maps.GetBoolean(*app.Labels, types.TLB_ENABLED, false) {
				log.Printf("Adding new app - %s\n", app.ID)
				m.appUpdate <- &types.AppInfo{
					AppId:  app.ID,
					Labels: *app.Labels,
				}
				// add this app to the list of known apps
				m.appApp(app.ID, *app.Labels)
				for _, task := range app.Tasks {
					backendInfo := m.createBackendInfo(app.ID, task.IPAddresses, task.Ports)
					log.Printf("[DEBUG] Adding backend for %s as %v\n", app.ID, backendInfo.Node)
					m.addBackend <- backendInfo
				}
			}
		}
	}
}

func (m *MarathonProvider) containsApp(appId string) bool {
	_, present := m.apps[appId]
	return present
}

func (m *MarathonProvider) appApp(appId string, labels map[string]string) {
	m.apps[appId] = labels
}

func (m *MarathonProvider) createBackendInfo(appId string, ipAddresses []*marathon.IPAddress, ports []int) *types.BackendInfo {
	appLabels := m.apps[appId]
	portIndex := maps.GetInt(appLabels, types.TLB_PORTINDEX, 0)

	return &types.BackendInfo{
		AppId: appId,
		Node:  ipAddresses[portIndex].IPAddress + ":" + fmt.Sprintf("%d", ports[portIndex]),
	}
}
