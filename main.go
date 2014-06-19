package main

import (
	"fmt"
	"github.com/coreos/go-etcd/etcd"
	"github.com/facebookgo/flagenv"
	"github.com/jessevdk/go-flags"
	"log"

	"github.com/rcrowley/go-metrics"
	"github.com/rcrowley/go-metrics/librato"
	"regexp"
	"time"
	//	"net/http"
)

type Options struct {
	etcd_server   []string `short:"e" description:"Etcd Server url. Multiple servers can be specified" default:"http://localhost:4001"`
	sensu_server  string   `short:"s" description:Sensu server url default:"http://localhost:8080"`
	key_prefix    string   `short:"k" description:"the prefix to use in etcd for storing check urls" default:"/urlmon"`
	librato_user  string   `short:"u" description:"librato user"`
	librato_token string   `short:"t" description:"librato token"`
}

type Check struct {
	Id           string
	URL          string
	Registry     metrics.Registry
	Content      string
	ContentRegex regexp.Regexp
}

var Checks []Check
var opts Options
var parser = flags.NewParser(&opts, flags.Default)
var UseUpperCaseFlagNames = true

// libratoMetrics starts the goroutine for reporting metrics on this check to librato
func (c *Check) libratoMetrics(done <-chan struct{}) {
	go func() {
		select {
		case <-done:
			return
		default:
			// start the metrics collector should be per-check
			librato.Librato(c.Registry,
				60*time.Second,     // interval
				opts.librato_user,  // account owner email address
				opts.librato_token, // Librato API token
				// This should be per check I think
				c.Id,             // source
				[]float64{95},    // precentiles to send
				time.Millisecond, // time unit
			)
		}
	}()
}

func loadChecks(client *etcd.Client) {
	done := make(chan struct{})

	// create etcd watch chan for reparsing config
	resp, err := client.Get(fmt.Sprint("%s/checks", opts.key_prefix), false, false)
	if err != nil {
		log.Fatalf("Problem fetching Checks from etcd: %s", err)
	}

	for _, n := range resp.Node.Nodes {
		// if its first time and we have no checks load it up
		if len(Checks) > 0 {
			log.Printf("%s: %s\n", n.Key, n.Value)
		} else {
			for _, _ = range Checks {
				done <- struct{}{}
			}
			log.Printf("%s: %s\n", n.Key, n.Value)
		}
	}
}

func main() {
	flagenv.Parse()
	parser.Parse()

	etcdClient := etcd.NewClient(opts.etcd_server)
	watchChan := make(chan *etcd.Response)
	go etcdClient.Watch(fmt.Sprintf("%s/", opts.key_prefix), 0, false, watchChan, nil)
	loadChecks(etcdClient)

	// loop and reload checks when etcd changes
	for {
		<-watchChan
		loadChecks(etcdClient)
	}
}
