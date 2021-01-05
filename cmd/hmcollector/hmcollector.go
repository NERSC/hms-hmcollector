// Copyright 2020 Hewlett Packard Enterprise Development LP

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap/zapcore"
	"stash.us.cray.com/HMS/hms-hmcollector/internal/http_logger"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/namsral/flag"
	"go.uber.org/zap"

	"stash.us.cray.com/HMS/hms-hmcollector/internal/hmcollector"
	"stash.us.cray.com/HMS/hms-hmcollector/internal/river_collector"
	rf "stash.us.cray.com/HMS/hms-smd/pkg/redfish"
	"stash.us.cray.com/HMS/hms-certs/pkg/hms_certs"
)

const NumWorkers = 30
const EndpointRefreshInterval = 5

var telemetryTypes = [2]river_collector.TelemetryType{
	river_collector.TelemetryTypePower,
	river_collector.TelemetryTypeThermal,
}

var (
	//namsral flag parsing, also parses from env vars that are upper case
	pollingEnabled     = flag.Bool("polling_enabled", false, "Should polling be enabled?")
	rfSubscribeEnabled = flag.Bool("rf_subscribe_enabled", false,
		"Should redfish subscribing be enabled?")
	rfStreamingEnabled = flag.Bool("rf_streaming_enabled", true,
		"Should streaming telemetry subscriptions be created?")
	restEnabled = flag.Bool("rest_enabled", true, "Should a RESTful server be started?")

	VaultEnabled = flag.Bool("vault_enabled", true, "Should vault be used for credentials?")
	VaultAddr    = flag.String("vault_addr", "http://localhost:8200", "Address of Vault.")
	VaultKeypath = flag.String("vault_keypath", "secret/hms-creds",
		"Keypath for Vault credentials.")

	kafkaBrokersConfigFile = flag.String("kafka_brokers_config", "configs/kafka_brokers.json",
		"Path to the configuration file containing all of the Kafka brokers this collector should produce to.")

	pollingInterval    = flag.Int("polling_interval", 10, "The polling interval to use in seconds.")
	hsmRefreshInterval = flag.Int("hsm_refresh_interval", 30,
		"The interval to check HSM for new Redfish Endpoints in seconds.")

	smURL    = flag.String("sm_url", "", "Address of the State Manager.")
	restURL  = flag.String("rest_url", "", "Address for Redfish events to target.")
	restPort = flag.Int("rest_port", 80, "The port the REST interface listens on.")
	caURI    = flag.String("hmcollector_ca_uri","","URI of the CA cert bundle.")
	logInsecFailover = flag.Bool("hmcollector_log_insecure_failover",true,"Log/don't log TLS insecure failovers.")
	httpTimeout = flag.Int("http_timeout",10,"Timeout in seconds for HTTP operations.")

	// This is really a hacky option that should only be used when incoming timestamps can't be trusted.
	// For example, if NTP isn't working and the controllers are reporting their time as from 1970.
	IgnoreProvidedTimestamp = flag.Bool("ignore_provided_timestamp", false,
		"Should the collector disregard any provided timestamps and instead use a local value of NOW?")

	kafkaBrokers []*hmcollector.KafkaBroker

	Running = true

	RestSRV   *http.Server = nil
	WaitGroup sync.WaitGroup

	ctx context.Context

	httpClient *retryablehttp.Client
	rfClient *hms_certs.HTTPClientPair
	rfClientLock sync.RWMutex

	atomicLevel zap.AtomicLevel
	logger      *zap.Logger

	RFSubscribeShutdown chan bool
	PollingShutdown     chan bool

	hsmEndpointRefreshShutdown chan bool
	HSMEndpoints               map[string]*rf.RedfishEPDescription
)

type EndpointWithCollector struct {
	Endpoint       *rf.RedfishEPDescription
	RiverCollector river_collector.RiverCollector
	LastContacted  *time.Time
	Model          string
}

type jsonPayload struct {
	payload string
	topic   string
}

func doUpdateHSMEndpoints() {
	for Running {
		// Get Redfish endpoints from HSM
		newEndpoints, newEndpointsErr := hmcollector.GetEndpointList(httpClient, *smURL)
		if newEndpoints == nil || len(newEndpoints) == 0 || newEndpointsErr != nil {
			// Ignore and retry on next interval.
			logger.Warn("No endpoints retrieved from State Manager", zap.Error(newEndpointsErr))
		} else {
			for endpointIndex, _ := range newEndpoints {
				newEndpoint := newEndpoints[endpointIndex]

				// Make sure this is a new endpoint.
				if _, endpointIsKnown := HSMEndpoints[newEndpoint.ID]; endpointIsKnown {
					continue
				}
				// No point in wasting our time trying to talk to endpoints HSM wasn't able to.
				if newEndpoint.DiscInfo.LastStatus != "DiscoverOK" {
					logger.Warn("Ignoring endpoint because HSM status not DiscoveredOK",
						zap.Any("newEndpoint", newEndpoint))
					continue
				}

				if *VaultEnabled {
					// Lookup the credentials if we have Vault enabled.
					updateErr := updateEndpointWithCredentials(&newEndpoint)

					if updateErr != nil {
						logger.Error("Unable to update credentials for endpoint with those retrieved from Vault",
							zap.Error(updateErr),
							zap.Any("newEndpoint", newEndpoint))

						// Ignore this endpoint for now, maybe the situation will improve the next time around.
						continue
					} else {
						logger.Debug("Updated endpoint credentials with those retrieved from Vault",
							zap.Any("newEndpoint", newEndpoint))
					}
				}

				HSMEndpoints[newEndpoint.ID] = &newEndpoint
			}
		}

		// Use a channel in case we have long refresh intervals so we don't wait around for things to exit.
		select {
		case <-hsmEndpointRefreshShutdown:
			break
		case <-time.After(time.Duration(*hsmRefreshInterval) * time.Second):
			continue
		}
	}

	logger.Info("HSM endpoint monitoring routine shutdown.")
}

func setupLogging() {
	logLevel := os.Getenv("LOG_LEVEL")
	logLevel = strings.ToUpper(logLevel)

	atomicLevel = zap.NewAtomicLevel()

	encoderCfg := zap.NewProductionEncoderConfig()
	logger = zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderCfg),
		zapcore.Lock(os.Stdout),
		atomicLevel,
	))

	switch logLevel {
	case "DEBUG":
		atomicLevel.SetLevel(zap.DebugLevel)
	case "INFO":
		atomicLevel.SetLevel(zap.InfoLevel)
	case "WARN":
		atomicLevel.SetLevel(zap.WarnLevel)
	case "ERROR":
		atomicLevel.SetLevel(zap.ErrorLevel)
	case "FATAL":
		atomicLevel.SetLevel(zap.FatalLevel)
	case "PANIC":
		atomicLevel.SetLevel(zap.PanicLevel)
	default:
		atomicLevel.SetLevel(zap.InfoLevel)
	}
}

// This function is used to set up an HTTP validated/non-validated client
// pair for Redfish operations.  This is done at the start of things, and also 
// whenever the CA chain bundle is "rolled".

func createRFClient() error {
	//Wait for all reader locks to release, prevent new reader locks.  Once
	//we acquire this lock, all RF operations are blocked until we unlock.

	//For testing/debug only.

    envstr := os.Getenv("HMCOLLECTOR_CA_PKI_URL")
    if (envstr != "") {
        logger.Info("Using CA PKI URL: ",zap.String("",envstr))
        hms_certs.ConfigParams.VaultCAUrl = envstr
    }
    envstr = os.Getenv("HMCOLLECTOR_VAULT_PKI_URL")
    if (envstr != "") {
        logger.Info("Using VAULT PKI URL: ",zap.String("",envstr))
        hms_certs.ConfigParams.VaultPKIUrl = envstr
    }
    envstr = os.Getenv("HMCOLLECTOR_VAULT_JWT_FILE")
    if (envstr != "") {
        logger.Info("Using Vault JWT file: ",zap.String("",envstr))
        hms_certs.ConfigParams.VaultJWTFile = envstr
    }
    envstr = os.Getenv("HMCOLLECTOR_K8S_AUTH_URL")
    if (envstr != "") {
        logger.Info("Using K8S AUTH URL: ",zap.String("",envstr))
        hms_certs.ConfigParams.K8SAuthUrl = envstr
    }

	//Wait for all reader locks to release, prevent new reader locks.  Once
	//we acquire this lock, all RF operations are blocked until we unlock.

	rfClientLock.Lock()
	defer rfClientLock.Unlock()

	logger.Info("All RF threads paused.")
	if (*caURI != "") {
		logger.Info("Creating Redfish HTTP client with CA trust bundle from",
			zap.String("",*caURI))
	} else {
		logger.Info("Creating Redfish HTTP client without CA trust bundle.")
	}
	rfc,err := hms_certs.CreateHTTPClientPair(*caURI,*httpTimeout)
	if (err != nil) {
		return fmt.Errorf("ERROR: Can't create Redfish HTTP client: %v",err)
	}

	rfClient = rfc
	return nil
}

func caChangeCB(caBundle string) {
	logger.Info("CA bundle rolled; waiting for all RF threads to pause...")
	err := createRFClient()
	if (err != nil) {
		logger.Error("Can't create TLS-verified HTTP client pair with cert roll, using previous one: ",zap.Error(err))
	} else {
		logger.Info("HTTP transports/clients now set up with new CA bundle.")
	}
}


func main() {
	setupLogging()

	// Parse the arguments.
	flag.Parse()

	logger.Info("hmcollector starting...", zap.Any("logLevel", atomicLevel))

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(context.Background())

	// For performance reasons we'll keep the client that was created for this base request and reuse it later.
	httpClient = retryablehttp.NewClient()
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	httpClient.HTTPClient.Transport = transport

	hms_certs.ConfigParams.LogInsecureFailover = *logInsecFailover
	hms_certs.Init(nil)

	//Create a TLS-verified HTTP client for Redfish stuff.  Try for a while,
	//fail over if CA-enabled transports can't be created.

	ok := false

	for ix := 1; ix <= 10; ix ++ {
		err := createRFClient()
		if (err == nil) {
			logger.Info("Redfish HTTP client pair creation succeeded.")
			ok = true
			break
		}

		logger.Error("TLS-verified client pair creation failure, ",
			zap.Int("attempt",ix),zap.Error(err))
		time.Sleep(2 * time.Second)
	}

	if (!ok) {
		logger.Error("Can't create secure HTTP client pair for Redfish, exhausted retries.")
		logger.Error("   Making insecure client; Please check CA bundle URI for correctness.")
		*caURI = ""
		err := createRFClient()
		if (err != nil) {
			//Should never happen!!
			panic("Can't create Redfish HTTP client!!!")
		}
	}

	if (*caURI != "") {
		err := hms_certs.CAUpdateRegister(*caURI,caChangeCB)
		if (err != nil) {
            logger.Warn("Unable to register CA bundle watcher for ",
                zap.String("URI",*caURI),zap.Error(err))
            logger.Warn("   This means no updates when CA bundle is rolled.")
        }
    } else {
       logger.Warn("No CA bundle URI specified, not watching for CA changes.")
    }

	// Also, since we're using logger it make sense to set the logger to use the one we've already setup.
	httpLogger := http_logger.NewHTTPLogger(logger)
	httpClient.Logger = httpLogger

	if *restEnabled {
		// Only enable handling of the root URL if REST is "enabled".
		http.HandleFunc("/", parseRequest)

		logger.Info("REST collection endpoint enabled.")
	}

	// Because we need our liveness/readiness probes to always work, we always setup a HTTP server.
	// NOTE: start the rest server here so we don't die before initialization happens
	WaitGroup.Add(1) // add the wait that main will sit on later
	logger.Info("Starting rest server.")
	doRest()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	hsmEndpointRefreshShutdown = make(chan bool)
	RFSubscribeShutdown = make(chan bool)
	PollingShutdown = make(chan bool)

	go func() {
		<-c
		Running = false

		// Cancel the context to cancel any in progress HTTP requests.
		cancel()

		if *pollingEnabled || *rfSubscribeEnabled {
			hsmEndpointRefreshShutdown <- true
		}

		if *rfSubscribeEnabled {
			RFSubscribeShutdown <- true
		}
		if *pollingEnabled {
			PollingShutdown <- true
		}

		if RestSRV != nil {
			if err := RestSRV.Shutdown(nil); err != nil {
				logger.Panic("Unable to stop REST collection server!", zap.Error(err))
			}
		}
	}()

	HSMEndpoints = make(map[string]*rf.RedfishEPDescription)

	// NOTE: will wait within this function forever if vault doesn't connect
	if *VaultEnabled {
		setupVault()
	}

	// NOTE: will wait within this function forever if kafka doesn't connect
	setupKafka()

	// Always need to keep an up-to-date list of Redfish endpoints assuming there is a mode enabled that can use it.
	if *pollingEnabled || *rfSubscribeEnabled {
		go doUpdateHSMEndpoints()
	}

	if *pollingEnabled {
		if *smURL == "" {
			logger.Panic("State Manager URL can NOT be empty")
		}
		WaitGroup.Add(1)

		logger.Info("Polling enabled.")

		gigabyteCollector = river_collector.GigabyteRiverCollector{}
		intelCollector = river_collector.IntelRiverCollector{}
		hpeCollector = river_collector.HPERiverCollector{}

		go doPolling()
	}

	if *rfSubscribeEnabled {
		if *smURL == "" {
			logger.Panic("State Manager URL can NOT be empty!")
		}
		if *restURL == "" {
			logger.Panic("Redfish event target URL can NOT be empty!")
		}
		WaitGroup.Add(1)

		logger.Info("Redfish Event Subscribing enabled.")

		go doRFSubscribe()
	}

	// We'll spend pretty much the rest of life blocking on the next line.
	WaitGroup.Wait()

	// Close the connection to Kafka to make sure any buffered data gets flushed.
	defer func() {
		for idx := range kafkaBrokers {
			thisBroker := kafkaBrokers[idx]

			// This call to Flush is given a maximum timeout of 15 seconds (which is entirely arbitrary and should
			// never take that long). It's very likely this will return almost immediately in most cases.
			abandonedMessages := thisBroker.KafkaProducer.Flush(15 * 1000)
			logger.Info("Closed connection with Kafka broker.",
				zap.Any("broker", thisBroker),
				zap.Int("abandonedMessages", abandonedMessages))
		}

	}()

	// Cleanup any leftover connections...because Go.
	httpClient.HTTPClient.CloseIdleConnections()
	rfClient.CloseIdleConnections()

	logger.Info("Exiting...")

	_ = logger.Sync()
}