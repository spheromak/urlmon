package main

import (
	"fmt"
	"github.com/coreos/go-etcd/etcd"
	"github.com/davecgh/go-spew/spew"
	"github.com/facebookgo/flagenv"
	"github.com/jessevdk/go-flags"
	"github.com/rcrowley/go-metrics"
	"github.com/rcrowley/go-metrics/librato"
	"log"
	"path"
	"strings"
	//	"net/http"
	"net/url"
	"os"
	"regexp"
	"time"
)

type Options struct {
	EtcdServer   []string `short:"e" long:"etcd-server" description:"Etcd Server url. Multiple servers can be specified" default:"http://localhost:4001"`
	KeyPrefix    string   `short:"k" long:"etcd-key" description:"the prefix to use in etcd for storing check urls" default:"/urlmon"`
	LibratoUser  string   `short:"u" long:"librato-user" description:"librato user"`
	LibratoToken string   `short:"t" long:"librato-token"  description:"librato token"`
	SensuServer  string   `short:"s" long:"sensu-server" description:"Sensu server url" default:"http://localhost:8080"`
}

type Check struct {
	Id           string
	URL          *url.URL
	Registry     metrics.Registry
	Content      string
	Level        string
	ContentRegex *regexp.Regexp
}

var (
	Checks                []Check
	opts                  Options
	UseUpperCaseFlagNames = true
)

// libratoMetrics starts the goroutine for reporting metrics on this check to librato
func (c *Check) libratoMetrics(done *chan struct{}) {
	go func() {
		select {
		case <-*done:
			return
		default:
			// start the metrics collector should be per-check
			librato.Librato(c.Registry,
				60*time.Second,    // interval
				opts.LibratoUser,  // account owner email address
				opts.LibratoToken, // Librato API token
				// This should be per check I think
				c.Id,             // source
				[]float64{95},    // precentiles to send
				time.Millisecond, // time unit
			)
		}
	}()
}

// createCheck populates check struct with data from etcd check
func createCheck(node *etcd.Node, done *chan struct{}) *Check {
	// create a check
	c := Check{Id: path.Base(node.Key)}
	for _, child := range node.Nodes {
		switch strings.ToUpper(path.Base(child.Key)) {
		case "URL":
			u, err := url.Parse(child.Value)
			if err != nil {
				log.Fatalf("Url isn't valid for key: %s, %s", node.Key, err)
			}
			c.URL = u
		case "CONTENT":
			c.Content = child.Value
		case "REGEX":
			r, err := regexp.Compile(child.Value)
			if err != nil {
				log.Fatalf("couldn't compile regex for Check: %s, %s", node.Key, err)
			}
			c.ContentRegex = r
		case "LEVEL":
			c.Level = strings.ToUpper(node.Value)
		}
	}

	if opts.LibratoUser != "" {
		c.libratoMetrics(done)
	}
	return &c
}

// loadChecks reads etcd and populates the Checks Slice
func loadChecks(client *etcd.Client, done *chan struct{}) {
	// create etcd watch chan for reparsing config
	resp, err := client.Get(fmt.Sprintf("%s/checks", opts.KeyPrefix), true, true)
	if err != nil {
		log.Fatalf("Problem fetching Checks from etcd: %s", err)
	}

	Checks = nil // this clears the slice. we will have to realloc that mem, but gc should get round to it.
	for _, n := range resp.Node.Nodes {
		// these top level nodes should be a directory
		if !n.Dir {
			log.Printf("Error loading config %s is not a dir, skipping", n.Key)
			continue
		}

		// if Checks is populated then we want to shutdown monitors and rebuild
		// only if we should be setting up librato metrics
		if opts.LibratoUser != "" {
			log.Println("waiting for checkers to stop")
			for _, _ = range Checks {
				*done <- struct{}{}
			}
		}
		log.Printf("Loading: %s: %s\n", n.Key, n.Value)
		Checks = append(Checks, *createCheck(n, done))
	}

	spew.Dump(Checks)
}

// Create our Etcd Dir structure
func setupEtcd(client *etcd.Client) {
	for _, path := range []string{opts.KeyPrefix, fmt.Sprintf("%s/checks", opts.KeyPrefix)} {
		if _, err := client.Get(path, false, false); err != nil {
			log.Printf("Creating dir in etcd: %s ", path)
			if _, err := client.CreateDir(path, 0); err != nil {
				log.Fatalf("Couldn't create etcd dir: %s, %s ", err)
			}
		}
	}
}

func main() {
	if _, err := flags.Parse(&opts); err != nil {
		if err.(*flags.Error).Type == flags.ErrHelp {
			os.Exit(0)
		} else {
			log.Println(err)
			os.Exit(1)
		}
	}

	etcdClient := etcd.NewClient(opts.EtcdServer)
	setupEtcd(etcdClient)

	watchChan := make(chan *etcd.Response)
	// setup recursive watch of the keyspace
	go etcdClient.Watch(opts.KeyPrefix, 0, true, watchChan, nil)

	done := make(chan struct{})
	loadChecks(etcdClient, &done)

	// loop and reload checks when etcd changes
	for {
		r := <-watchChan
		log.Printf("Reloading checks from etcd, triggered by '%s' on '%s' with value: '%s' ", r.Action, r.Node.Key, r.Node.Value)
		loadChecks(etcdClient, &done)
	}
}
