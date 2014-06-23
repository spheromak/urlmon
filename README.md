Urlmon - etcd backed concurrent url monitor that alerts to sensu, and emits metrics to librato

This scratches a specific itch I had.  Backend and alerting may be interfaced out in the future. Right now this isminimal impl to solve an immediate need.

## Check format
Check format in etcd:

    /prefix/checkID/
      - url   (required)
      - content
      - interval 
      - spay   


## Status UI
Theres a simple status ui that emits json for each check that's running. By default it's bound to :9731

   

  
