package discovery

import (
	"log"

	"github.com/flier/curator.go"
)

type ServiceDiscovery struct {
	client curator.CuratorFramework

	// Cache of watched services
	Services map[string][]*ServiceInstance

	// Maintained service registrations
	maintain map[string]*ServiceInstance

	tree *TreeCache

	// path under which to read/create registrations (/base/servicename/instance-id)
	basePath string

	serializer InstanceSerializer

	connChanges chan bool
}

func NewServiceDiscovery(client curator.CuratorFramework, basePath string) *ServiceDiscovery {
	s := new(ServiceDiscovery)
	s.client = client
	s.basePath = basePath
	s.maintain = make(map[string]*ServiceInstance)
	s.serializer = &JsonInstanceSerializer{}
	s.connChanges = make(chan bool, 10)
	s.Services = make(map[string][]*ServiceInstance)
	return s
}

func (s *ServiceDiscovery) MaintainRegistrations() error {
	go s.maintainConn()
	s.client.ConnectionStateListenable().AddListener(s)
	return nil
}

func (s *ServiceDiscovery) Watch() error {
	if err := curator.NewEnsurePath(s.basePath).Ensure(s.client.ZookeeperClient()); err != nil {
		return err
	}
	s.tree = NewTreeCache(s)
	s.tree.Start()
	return nil
}

func (s *ServiceDiscovery) StateChanged(c curator.CuratorFramework, n curator.ConnectionState) {
	s.connChanges <- n.Connected()
}

func (s *ServiceDiscovery) maintainConn() {
	prev := false
	for {
		// wait for conn change
		c, ok := getMostRecentBool(s.connChanges)
		if !ok {
			break
		}
		if c && c != prev {
			log.Println("Reconnected. Re-registering services.")
			s.ReregisterAll()
		}
		prev = c
	}
}

func (s *ServiceDiscovery) pathForName(name string) string {
	return curator.JoinPath(s.basePath, name)
}

func (s *ServiceDiscovery) pathForInstance(name, id string) string {
	return curator.JoinPath(s.pathForName(name), id)
}

func (s *ServiceDiscovery) Register(service *ServiceInstance) error {
	b, err := s.serializer.Serialize(service)
	if err != nil {
		return err
	}

	p := s.pathForInstance(service.Name, service.Id)

	m := curator.PERSISTENT
	if service.ServiceType == DYNAMIC {
		m = curator.EPHEMERAL
	}

	for i := 0; i < 3; i++ {
		log.Printf("Creating %s registration %s (attempt %d): %s\n", service.Name, service.Spec(), i+1, p)
		_, err = s.client.Create().CreatingParentsIfNeeded().WithMode(m).ForPathWithData(p, b)
		if err == nil {
			s.maintain[service.Id] = service
			return nil
		}
	}

	return err
}

func (s *ServiceDiscovery) Unregister(service *ServiceInstance) error {
	p := s.pathForInstance(service.Name, service.Id)
	delete(s.maintain, service.Id)

	log.Printf("Deleting %s registration %s: %s\n", service.Name, service.Spec(), p)
	return s.client.Delete().ForPath(p)
}

func (s *ServiceDiscovery) ReregisterAll() error {
	for _, i := range s.maintain {
		if err := s.Register(i); err != nil {
			return err
		}
	}
	return nil
}

func (s *ServiceDiscovery) UnregisterAll() error {
	for _, i := range s.maintain {
		if err := s.Unregister(i); err != nil {
			return err
		}
	}
	return nil
}
