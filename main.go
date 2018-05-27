package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/linnv/manhelp"
)

const (
	dialTimeout      = time.Second * 10
	hearbeatInterval = time.Second * 3
)

const (
	prefix          = "_sd/"
	routerHeartbeat = "/heartbeat"
)

type NodeStatus int

const (
	Running NodeStatus = 1
	Offline NodeStatus = 2
)

type SDInfo struct {
	Name   string     `json:"Name"`
	NodeID string     `json:"NodeID"`
	Host   string     `json:"Host"`
	Status NodeStatus `json:"State"`
	// ...more
}

func (sd *SDInfo) Reset() {
	sd.Name = ""
	sd.NodeID = ""
	sd.Status = Running
}

type Master struct {
	Done        chan struct{}
	Nodes       map[string]*SDInfo
	m           *sync.RWMutex
	wg          *sync.WaitGroup
	heartbeatWg *sync.WaitGroup

	netClient *http.Client
}

func NewMaster() *Master {
	return &Master{
		Done:        make(chan struct{}),
		Nodes:       make(map[string]*SDInfo, 2),
		m:           new(sync.RWMutex),
		wg:          new(sync.WaitGroup),
		heartbeatWg: new(sync.WaitGroup),
		netClient: &http.Client{
			Timeout: time.Second * 3,
		},
	}
}

func (m *Master) DelNode(id string) {
	m.m.Lock()
	delete(m.Nodes, id)
	m.m.Unlock()
}

func (m *Master) upsertNode(node *SDInfo) {
	if node == nil {
		return
	}
	m.m.Lock()
	defer m.m.Unlock()
	m.Nodes[node.NodeID] = node

}

func (m *Master) checkHeartbeat(nodes []*SDInfo) {
	if len(nodes) < 1 {
		return
	}

	failedID := make([]string, 0, len(nodes))
	m.m.RLock()
	for _, v := range nodes {
		resp, err := m.netClient.Get(v.Host + routerHeartbeat)
		if err != nil {
			log.Printf("err: %+v\n", err)
			failedID = append(failedID, v.NodeID)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			failedID = append(failedID, v.NodeID)
		}
	}
	m.m.RUnlock()

	if len(failedID) > 0 {
		m.m.Lock()
		for i := 0; i < len(failedID); i++ {
			m.Nodes[failedID[i]].Status = Offline
		}
		m.m.Unlock()
	}
	m.heartbeatWg.Done()
}

func (m *Master) CheckHeartbeat() {
	m.wg.Add(1)
	defer m.wg.Done()
	ticker := time.NewTicker(hearbeatInterval)
	for {
		select {
		case <-ticker.C:
			m.m.RLock()
			lenNode := len(m.Nodes)
			if lenNode < 1 {
				m.m.RUnlock()
				continue
			}

			const jobsPerNode = 20
			enableGorutines := lenNode / jobsPerNode
			if lenNode%jobsPerNode > 0 {
				enableGorutines++
			}
			goMap := make(map[int][]*SDInfo, enableGorutines)
			i := 0
			for _, v := range m.Nodes {
				i++
				nodeIndex := i / jobsPerNode
				goMap[nodeIndex] = append(goMap[nodeIndex], v)
			}
			m.m.RUnlock()

			for i := 0; i < len(goMap); i++ {
				m.heartbeatWg.Add(1)
				go m.checkHeartbeat(goMap[i])
			}
			m.heartbeatWg.Wait()
		case <-m.Done:
			return
		}
	}
}

func (m *Master) Exit() {
	close(m.Done)
	m.wg.Wait()
}

func (m *Master) Run() {
	go m.CheckHeartbeat()
	go m.WatchChange()
}

func (m *Master) GetNodes() []byte {
	m.m.RLock()
	defer m.m.RUnlock()
	bs, err := json.Marshal(m.Nodes)
	if err != nil {
		return nil
	}
	return bs
}

var configEctdEndpoints = []string{"http://192.168.100.243:2479"}
var configHostIP = "192.168.99.1"

func (m *Master) WatchChange() {
	m.wg.Add(1)
	defer m.wg.Done()
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   configEctdEndpoints,
		DialTimeout: dialTimeout,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer cli.Close()

	rch := cli.Watch(context.Background(), prefix, clientv3.WithPrefix())
	for {
		select {
		case <-m.Done:
			return
		case wresp := <-rch:
			for _, ev := range wresp.Events {
				one := &SDInfo{}
				err := json.Unmarshal(ev.Kv.Value, &one)
				if err != nil {
					log.Printf("err: %+v\n", err)
					continue
				}
				one.Host = strings.TrimSuffix(one.Host, "/")
				if !strings.HasSuffix(one.Host, "http") {
					one.Host = "http://" + one.Host
				}
				fmt.Printf("%s %q : %q\n", ev.Type, ev.Kv.Key, ev.Kv.Value)
				m.upsertNode(one)
			}
		}
	}
}

func master() {
	var master = NewMaster()
	master.Run()

	http.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write(master.GetNodes())
		}
		if r.Method == http.MethodDelete {
			id := r.FormValue("id")
			if len(id) > 0 {
				master.DelNode(id)
				w.Write(master.GetNodes())
			}
			log.Printf("id: %+v\n", id)

		}
	})
	const port = ":9091"
	server := http.Server{
		Addr:    port,
		Handler: http.DefaultServeMux,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil {
			if err == http.ErrServerClosed {
			} else {
				panic(err)
			}
		}
	}()

	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, os.Kill)
	log.Print("http server listen on port ", port)
	log.Printf("use c-c to exit: \n")
	<-sigChan

	server.Shutdown(nil)
	master.Exit()
}

var nodePort string

func node() {
	http.HandleFunc(routerHeartbeat, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	var clientHost = configHostIP + ":" + nodePort
	r := rand.New(rand.NewSource(time.Now().Unix()))
	for i := 0; i < 10; i++ {
		nodeInfo := SDInfo{
			Name:   "sample",
			NodeID: "node-" + strconv.Itoa(r.Int()),
			Host:   clientHost,
			Status: Running,
		}
		bs, err := json.Marshal(nodeInfo)
		if err != nil {
			continue
		}

		cli, err := clientv3.New(clientv3.Config{
			Endpoints:   configEctdEndpoints,
			DialTimeout: dialTimeout,
		})
		if err != nil {
			log.Fatal(err)
		}
		defer cli.Close()

		_, err = cli.Put(context.Background(), prefix+nodeInfo.NodeID, string(bs))
		if err != nil {
			log.Printf("err: %+v\n", err)
			continue
		}
		break

		time.Sleep(time.Duration(i) * time.Second * 2)
	}
	server := http.Server{
		Addr:    ":" + nodePort,
		Handler: http.DefaultServeMux,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil {
			if err == http.ErrServerClosed {
			} else {
				panic(err)
			}
		}
	}()

	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, os.Kill)
	log.Print("client:http server listen on port ", nodePort)
	log.Print("use c-c to exit: \n")
	<-sigChan
	server.Shutdown(nil)
}

func main() {
	runType := flag.String("t", "master", "run as master: master* or others as node")
	flag.StringVar(&nodePort, "port", "9092", "port of node")
	flag.Parse()

	if !flag.Parsed() {
		os.Stderr.Write([]byte("ERROR: logging before flag.Parse"))
		return
	}

	manhelp.BasicManHelpMain()

	if strings.Contains(*runType, "master") {
		master()
		return
	}
	log.Printf("run as node\n")
	node()
}
