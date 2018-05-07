package gelf

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Graylog2/go-gelf/gelf"
	"github.com/gliderlabs/logspout/router"
)

var hostname string

func init() {
	hostname, _ = hostnameFromRancherMetadata()
	router.AdapterFactories.Register(NewGelfAdapter, "gelf")
}

func hostnameFromRancherMetadata() (hostname string, err error) {
	res, err := http.Get("http://rancher-metadata.rancher.internal/latest/self/host/name")
	if err == nil {
		resBytes, err := ioutil.ReadAll(res.Body)
		if err == nil {
			hostname = string(resBytes)
		}
	}

	if len(hostname) == 0 {
		hostname, err = os.Hostname()
	}
	return
}

// GelfAdapter is an adapter that streams UDP JSON to Graylog
type GelfAdapter struct {
	writer *gelf.Writer
	route  *router.Route
}

// NewGelfAdapter creates a GelfAdapter with UDP as the default transport.
func NewGelfAdapter(route *router.Route) (router.LogAdapter, error) {
	_, found := router.AdapterTransports.Lookup(route.AdapterTransport("udp"))
	if !found {
		return nil, errors.New("unable to find adapter: " + route.Adapter)
	}

	gelfWriter, err := gelf.NewWriter(route.Address)
	if err != nil {
		return nil, err
	}

	return &GelfAdapter{
		route:  route,
		writer: gelfWriter,
	}, nil
}

// Stream implements the router.LogAdapter interface.
func (a *GelfAdapter) Stream(logstream chan *router.Message) {
	for message := range logstream {
		m := &GelfMessage{message}

		level := gelf.LOG_INFO
		if m.Source == "stderr" {
			level = gelf.LOG_ERR
		}
		extra, err := m.getExtraFields()
		if err != nil {
			log.Println("Graylog:", err)
			continue
		}

		host := m.Container.Config.Labels["io.rancher.container.name"]
		if len(host) == 0 {
			host = hostname
		}

		msg := gelf.Message{
			Version:  "1.1",
			Host:     host,
			Short:    m.Message.Data,
			TimeUnix: float64(m.Message.Time.UnixNano()/int64(time.Millisecond)) / 1000.0,
			Level:    level,
			RawExtra: extra,
		}

		if msg.Short == "" {
			msg.Short = "None"
		}

		// here be message write.
		if err := a.writer.WriteMessage(&msg); err != nil {
			log.Println("Graylog:", err)
			continue
		}
	}
}

type GelfMessage struct {
	*router.Message
}

func (m GelfMessage) getExtraFields() (json.RawMessage, error) {
	logspoutInstance, _ := os.Hostname()
	extra := map[string]interface{}{
		"_container_id":          m.Container.ID,
		"_container_name":        m.Container.Name[1:], // might be better to use strings.TrimLeft() to remove the first /
		"_image_id":              m.Container.Image,
		"_image_name":            m.Container.Config.Image,
		"_command":               strings.Join(m.Container.Config.Cmd[:], " "),
		"_created":               m.Container.Created,
		"_rancher_stack_service": m.Container.Config.Labels["io.rancher.stack_service.name"],
		"_rancher_host":          hostname,
		"_logspout_instance":     logspoutInstance,
		"_logspout_source":       m.Source,
	}
	for name, label := range m.Container.Config.Labels {
		if len(name) > 5 && strings.ToLower(name[0:5]) == "gelf_" {
			extra[name[4:]] = label
		}
	}
	swarmnode := m.Container.Node
	if swarmnode != nil {
		extra["_swarm_node"] = swarmnode.Name
	}

	rawExtra, err := json.Marshal(extra)
	if err != nil {
		return nil, err
	}
	return rawExtra, nil
}
