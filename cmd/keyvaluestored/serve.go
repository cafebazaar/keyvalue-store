package main

import (
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cafebazaar/keyvalue-store/internal/core"
	"github.com/pkg/profile"

	"github.com/cafebazaar/keyvalue-store/internal/engine"
	"github.com/cafebazaar/keyvalue-store/internal/voting"

	"github.com/go-redis/redis"

	redisBackend "github.com/cafebazaar/keyvalue-store/internal/backend/redis"
	staticCluster "github.com/cafebazaar/keyvalue-store/internal/cluster/static"
	redisTransport "github.com/cafebazaar/keyvalue-store/internal/transport/redis"
	"github.com/cafebazaar/keyvalue-store/pkg/keyvaluestore"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "start Server",
	Run:   serve,
}

var envPrefix *string

func init() {
	rootCmd.AddCommand(serveCmd)
	envPrefix = rootCmd.PersistentFlags().String("envprefix", "keyvaluestore", "prefix to use for environemnt variables")
}

func serve(cmd *cobra.Command, args []string) {
	config := loadConfigOrPanic(cmd)

	if config.Profiling {
		// Read following blog on Go profiling:
		// https://flaviocopes.com/golang-profiling/
		//
		// In order to export PDF:
		// go tool pprof --pdf keyvaluestored  /tmp/profile108564303/cpu.pprof  > file.pdf
		//
		// In order to view profiling in web:
		// go tool pprof -http 127.0.0.1:8080 keyvaluestored file2.pprof
		//
		// Other issues related to golang profiling:
		// https://github.com/golang/go/issues/18138
		defer profile.Start().Stop()
		log.Warn("PROFILING IS ENABLED")
	}
	cluster := configureClusterOrPanic(config)
	engine := configureEngineOrPanic(config)
	svc := getService(cluster, engine, config)

	server := makeRedisServerOrPanic(svc, config)
	startServerOrPanic(server)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs

	shutdownServerOrPanic(server)
}

func loadConfigOrPanic(cmd *cobra.Command) *Config {
	config, err := LoadConfig(cmd, *envPrefix)
	if err != nil {
		log.WithError(err).Panic("Failed to load configurations")
	}
	return config
}

func configureEngineOrPanic(config *Config) keyvaluestore.Engine {
	return engine.New(voting.New)
}

func configureClusterOrPanic(config *Config) keyvaluestore.Cluster {
	if config.StaticDiscovery != "" || config.LocalConnection != "" {
		return configureStaticDiscoveryClusterOrPanic(config)
	}

	log.Panicf("no suitable cluster formation available: %v", config)
	return nil
}

func configureStaticDiscoveryClusterOrPanic(config *Config) keyvaluestore.Cluster {
	hosts := strings.Split(config.StaticDiscovery, ",")
	var nodes []keyvaluestore.Backend

	for _, host := range hosts {
		nodes = append(nodes, connectToHostOrPanic(config, strings.TrimSpace(host)))
	}

	var options []staticCluster.Option

	if config.LocalConnection != "" {
		options = append(options,
			staticCluster.WithLocal(connectToHostOrPanic(config, config.LocalConnection)))
	}

	if config.Policy != "" {
		for _, policy := range convertPolicyListOrPanic(config.Policy) {
			options = append(options, staticCluster.WithPolicy(policy))
		}
	}

	return staticCluster.New(nodes, options...)
}

func connectToHostOrPanic(config *Config, host string) keyvaluestore.Backend {
	switch config.Backend {
	case "redis":
		return connectToRedisOrPanic(host)

	default:
		log.Panicf("unknown backend: %v", config.Backend)
		return nil
	}
}

func connectToRedisOrPanic(host string) keyvaluestore.Backend {
	client := redis.NewClient(&redis.Options{Addr: host})
	return redisBackend.New(client, host)
}

func getService(cluster keyvaluestore.Cluster,
	engine keyvaluestore.Engine,
	config *Config) keyvaluestore.Service {

	var options []core.Option
	if config.DefaultReadConsistency != "" {
		options = append(options,
			core.WithDefaultReadConsistency(convertConsistencyOrPanic(config.DefaultReadConsistency)))
	}
	if config.DefaultWriteConsistency != "" {
		options = append(options,
			core.WithDefaultWriteConsistency(convertConsistencyOrPanic(config.DefaultWriteConsistency)))
	}

	svc := core.New(cluster, engine, options...)

	return svc
}

func convertConsistencyOrPanic(consistency string) keyvaluestore.ConsistencyLevel {
	switch strings.ToLower(consistency) {
	case "1":
		return keyvaluestore.ConsistencyLevel_ONE

	case "one":
		return keyvaluestore.ConsistencyLevel_ONE

	case "all":
		return keyvaluestore.ConsistencyLevel_ALL

	case "majority":
		return keyvaluestore.ConsistencyLevel_MAJORITY

	default:
		log.Panicf("unrecognized consistency level: %v", consistency)
		return keyvaluestore.ConsistencyLevel_ALL
	}
}

func convertPolicyListOrPanic(policyList string) []keyvaluestore.Policy {
	items := strings.Split(policyList, ",")
	var result []keyvaluestore.Policy

	for _, item := range items {
		result = append(result, convertPolicyOrPanic(item))
	}

	return result
}

func convertPolicyOrPanic(policy string) keyvaluestore.Policy {
	switch strings.ToLower(policy) {
	case "readone-localorrandomnode":
		return keyvaluestore.PolicyReadOneLocalOrRandomNode

	case "readone-firstavailable":
		return keyvaluestore.PolicyReadOneFirstAvailable

	default:
		log.Panicf("unrecognized policy: %v", policy)
		return 0
	}
}

func makeRedisServerOrPanic(svc keyvaluestore.Service, config *Config) keyvaluestore.Server {
	readConsistency := keyvaluestore.ConsistencyLevel_MAJORITY
	writeConsistency := keyvaluestore.ConsistencyLevel_MAJORITY

	if config.DefaultReadConsistency != "" {
		readConsistency = convertConsistencyOrPanic(config.DefaultReadConsistency)
	}

	if config.DefaultWriteConsistency != "" {
		writeConsistency = convertConsistencyOrPanic(config.DefaultWriteConsistency)
	}

	return redisTransport.New(svc, config.RedisListenPort,
		time.Duration(config.RedisConnectionTimeout)*time.Millisecond,
		readConsistency, writeConsistency)
}

func startServerOrPanic(server keyvaluestore.Server) {
	err := server.Start()
	if err != nil {
		panicWithError(err, "failed to start server")
	}
}

func shutdownServerOrPanic(server keyvaluestore.Server) {
	if err := server.Close(); err != nil {
		panicWithError(err, "failed to close server")
	}
}

func panicWithError(err error, format string, args ...interface{}) {
	log.WithError(err).Panicf(format, args...)
}
