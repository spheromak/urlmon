package main

import (
	"encoding/json"
	"fmt"
	"github.com/coreos/go-etcd/etcd"
	_ "github.com/davecgh/go-spew/spew"
	"github.com/jessevdk/go-flags"
	"github.com/kelseyhightower/envconfig"
	"github.com/rcrowley/go-metrics"
	"github.com/rcrowley/go-metrics/librato"
	"log"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultInterval is the check check interval for a url specified in seconds
	DefaultInterval int = 15

	// DefaultSplay is the rand splay to introduce on checks so we don't flood
	DefaultSplay int = 5

	// DefaultAlertLevel is the level we alert at when no level is specified
	DefaultAlertLevel string = "WARN"
)

type Options struct {
	Etcd   []string `short:"e" long:"etcd" description:"Etcd Server url. Multiple servers can be specified" default:"http://localhost:4001"`
	Prefix string   `short:"p" long:"prefix" description:"the prefix to use in etcd for storing check urls" default:"/urlmon"`
	User   string   `short:"u" long:"user" description:"librato user"`
	Token  string   `short:"t" long:"token"  description:"librato token"`
	Port   int      `short:"P" long:"port"  description:"The port to start the status interface on" default:"9731"`
	Sensu  string   `short:"s" long:"sensu" description:"Sensu client address" default:"localhost:3030"`
}

type Check struct {
	Id           string
	URL          *url.URL
	Content      string
	Level        string
	ContentRegex *regexp.Regexp
	Interval     int
	Splay        int
	Registry     metrics.Registry
	shutdown     bool
}

type SensuEvent struct {
	Name     string   `json:name`
	Handlers []string `json:handlers`
	Output   string   `json:output`
	Status   int      `json:status`
}

type LazyResponse map[string]interface{}

var (
	Checks []Check
	opts   Options
)

func (c *Check) setupInterval() {
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}

	if c.Splay <= 0 {
		c.Splay = DefaultSplay
	}

	// build up splay and add to interval
	c.Interval += rand.Intn(c.Splay)
}

// reportStatus reports if
func (c *Check) reportStatus(status int, msg string) {
	s := "Success:"
	if status != 0 {
		s = "Fail:"
	}
	log.Println(s, c.Id, c.URL.String(), msg)

	// for now handler is hardcode TODO: move it to default/param/env
	event := SensuEvent{
		Name:     c.URL.String(),
		Handlers: []string{"hipchat"},
		Output:   msg,
		Status:   status,
	}

	go event.send()
}

// Monitor sets up the monitoring loop
func (c *Check) Monitor() {
	// build the splay/random timing into the interval
	c.setupInterval()

	// setup the guage
	g := metrics.NewGauge()

	metrics.Register(fmt.Sprintf("urlmon.request.%s", c.Id), g)
	// loop for shutdown and spaly/sleep
	for c.shutdown != true {
		// we sleep first here so that on startup we don't overwhelm
		s := time.Duration(c.Interval) * time.Second
		time.Sleep(s)

		// time.Since reports in nanoseocnds
		start := time.Now()
		r, err := http.Get(c.URL.String())
		g.Update(int64(time.Since(start)))
		if err != nil {
			c.reportStatus(1, err.Error())
		} else {
			r.Body.Close()
			c.reportStatus(0, r.Status)
		}
	}
}

// easy way to signal the monitor to shutdown
func (c *Check) Shutdown() {

	c.shutdown = true
}

// String Adds stringer interface to the lazy response
func (r LazyResponse) String() (s string) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		s = ""
		return
	}
	s = string(b)
	return
}

// send the sensu event to the local sensu port
func (e *SensuEvent) send() {
	conn, err := net.Dial("tcp", opts.Sensu)
	if err != nil {
		log.Println("Error connecting to sensu client socket.", err.Error())
		return
	}
	defer conn.Close()

	j, err := json.Marshal(e)
	if err != nil {
		log.Println("Error marshaling event data.", err.Error())
		return
	}
	fmt.Fprintf(conn, string(j))
}

// libratoMetrics starts the reporting metrics on this check to librato
func libratoMetrics() {
	host, err := os.Hostname()
	if err != nil {
		host = "localhost"
	}

	// start the metrics collector should be per-check
	log.Println("starting metrics")
	librato.Librato(metrics.DefaultRegistry,
		60*time.Second, // interval
		opts.User,      // account owner email address
		opts.Token,     // Librato API token
		// This should be per check I think
		host,             // source
		[]float64{95},    // precentiles to send
		time.Millisecond, // time unit
	)
}

// valueStr Converts a string to an int where a blank string or errvalue returns a 0
// This is a helper mostly for creating a Check where the interval or splay may be an empty string.
func valueStr(value string) int {
	v, err := strconv.Atoi(value)
	if err != nil {
		v = 0
	}
	return v
}

// createCheck populates check struct with data from etcd check
func createCheck(node *etcd.Node) (*Check, error) {
	// create a check
	c := Check{Id: path.Base(node.Key)}
	for _, child := range node.Nodes {
		log.Printf("  - %s", path.Base(child.Key))
		switch strings.ToUpper(path.Base(child.Key)) {
		case "URL":
			u, err := url.Parse(child.Value)
			if err != nil {
				log.Fatalf("Url isn't valid for key: %s, %s", node.Key, err.Error())
			}
			c.URL = u
		case "CONTENT":
			c.Content = child.Value
		case "REGEX":
			r, err := regexp.Compile(child.Value)
			if err != nil {
				log.Fatalf("couldn't compile regex for Check: %s, %s", node.Key, err.Error())
			}
			c.ContentRegex = r
		case "LEVEL":
			l := strings.ToUpper(child.Value)
			if l == "" {
				l = DefaultAlertLevel
			}
			c.Level = l
		case "SPLAY":
			c.Splay = valueStr(child.Value)
		case "INTERVAL":
			c.Interval = valueStr(child.Value)
		}
	}

	// set defaults
	if c.Splay == 0 {
		c.Splay = DefaultSplay
	}
	if c.Interval == 0 {
		c.Interval = DefaultInterval
	}
	if c.Level == "" {
		c.Level = DefaultAlertLevel
	}

	// BUG: Checks may be malformed in various ways, createCheck needs to implment more validations before returning the check
	return &c, nil
}

// loadChecks reads etcd popilates Checks, and starts their monitor
func loadChecks(client *etcd.Client) {
	// create etcd watch chan for reparsing config
	resp, err := client.Get(fmt.Sprintf("%s/checks", opts.Prefix), true, true)
	if err != nil {
		log.Fatalf("Problem fetching Checks from etcd: %s", err)
	}

	for _, n := range Checks {
		// signal that monitor to shutdown
		n.Shutdown()
	}
	// this clears the slice. we will have to realloc that mem, but gc should get round to it.
	Checks = nil

	for _, n := range resp.Node.Nodes {
		// these top level nodes should be a directory
		if !n.Dir {
			log.Printf("Error loading config %s is not a dir, skipping", n.Key)
			continue
		}

		log.Printf("Loading: %s: %s\n", n.Key, n.Value)
		c, err := createCheck(n)
		if err != nil {
			log.Println("Failed to create check, skipping: ", n.Key, err)
			continue
		}
		// start monitoring this check
		go c.Monitor()
		Checks = append(Checks, *c)
	}
}

// Create our Etcd Dir structure
func setupEtcd(client *etcd.Client) {
	for _, path := range []string{opts.Prefix, fmt.Sprintf("%s/checks", opts.Prefix)} {
		if _, err := client.Get(path, false, false); err != nil {
			log.Printf("Creating dir in etcd: %s ", path)
			if _, err := client.CreateDir(path, 0); err != nil {
				log.Fatalf("Couldn't create etcd dir: %s, %s ", err)
			}
		}
	}
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, LazyResponse{"checkdata": Checks})
}

func main() {
	err := envconfig.Process("urlmon", &opts)
	if err != nil {
		log.Fatalf("Error parsing ENV vars %s", err)
	}

	if _, err := flags.Parse(&opts); err != nil {
		if err.(*flags.Error).Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			log.Println(err.Error())
			os.Exit(1)
		}
	}

	etcdClient := etcd.NewClient(opts.Etcd)
	setupEtcd(etcdClient)

	watchChan := make(chan *etcd.Response)
	// setup recursive watch of the keyspace
	go etcdClient.Watch(opts.Prefix, 0, true, watchChan, nil)

	// start the metrics
	if opts.User != "" && opts.Token != "" {
		go libratoMetrics()
	}

	loadChecks(etcdClient)

	http.HandleFunc("/status", statusHandler)
	go http.ListenAndServe(fmt.Sprintf(":%d", opts.Port), nil)
	log.Printf("Status server up and running on ':%d'", opts.Port)

	// loop and reload checks when etcd changes
	for {
		r := <-watchChan
		log.Printf("Reloading checks from etcd, triggered by '%s' on '%s' with value: '%s' ", r.Action, r.Node.Key, r.Node.Value)
		loadChecks(etcdClient)
	}
}
