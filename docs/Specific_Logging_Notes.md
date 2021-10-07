# Notes on functional area specific logging of adapter-open-onu

To be able to perform detailed logging even under load conditions,
the possibility to configure specific log levels for different functional areas was implemented.
This was done by splitting the code into different packages.

Currently, logging can be configured separately for the following packages:

| package   | functional content |
| :------:  | :----------------------- |
| almgr     | utilities to manage alarm notifications |
| avcfg     | anig and vlan configuration functionality |
| common    | global utility functions and OMCI request handling |
| core      | RPC interfaces towards the core and device handler functionality | 
| devdb     | utilities for internal device ME DB |
| mib       | MIB upload/download and ONU device handling |
| omcitst   | OMCI test request handling |
| pmmgr     | utilities to manage onu metrics |
| swupg     | onu sw upgrade functionality |
| uniprt    | utilities for UNI port configuration |


## Configuration example

Set the log level to "DEBUG" for the "almgr" and "avcfg" packages and to "WARN" for all others.


### _Set adapter-open-onu default log level to “WARN”:_
```
$ voltctl log level list
COMPONENTNAME       PACKAGENAME    LEVEL
read-write-core     default        DEBUG
adapter-open-onu    default        DEBUG
adapter-open-olt    default        DEBUG
global              default        WARN

$ voltctl log level set WARN adapter-open-onu 
COMPONENTNAME       PACKAGENAME    STATUS     ERROR
adapter-open-onu    default        Success    

$ voltctl log level list
COMPONENTNAME       PACKAGENAME    LEVEL
adapter-open-olt    default        DEBUG
global              default        WARN
adapter-open-onu    default        WARN
read-write-core     default        DEBUG
```

### _Display existing log packages of adapter-open-onu:_ 

```
$ voltctl log package list adapter-open-onu
COMPONENTNAME       PACKAGENAME
adapter-open-onu    default
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/adapters/common
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/config
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/db
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/db/kvstore
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/events
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/flows
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/kafka
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/log
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/meters
adapter-open-onu    github.com/opencord/voltha-lib-go/v5/pkg/probe
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/almgr
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/avcfg
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/common
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/core
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/devdb
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/mib
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/omcitst
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/pmmgr
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/swupg
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/uniprt
adapter-open-onu    main
```

### _Set log level of packages “almgr” and “avcfg” to “DEBUG”:_ 

```
$ voltctl log level set DEBUG adapter-open-onu#github.com/opencord/voltha-openonu-adapter-go/internal/pkg/almgr
COMPONENTNAME       PACKAGENAME                                                         STATUS     ERROR
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/almgr    Success    

$ voltctl log level set DEBUG adapter-open-onu#github.com/opencord/voltha-openonu-adapter-go/internal/pkg/avcfg
COMPONENTNAME       PACKAGENAME                                                         STATUS     ERROR
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/avcfg    Success    

$  voltctl log level list adapter-open-onu
COMPONENTNAME       PACKAGENAME                                                         LEVEL
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/almgr    DEBUG
adapter-open-onu    github.com/opencord/voltha-openonu-adapter-go/internal/pkg/avcfg    DEBUG
adapter-open-onu    default                                                             WARN
```

### Note: In order to cancel the specific log level for individual packets, they have to be configured again with the globally set log level.