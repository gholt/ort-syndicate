package main

import (
	"fmt"
	"os"
	"sort"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/gholt/ring"
	pb "github.com/pandemicsyn/ort-syndicate/api/proto"

	"log"
	"net"
	"path/filepath"
	"strings"
)

// FatalIf is just a lazy log/panic on error func
func FatalIf(err error, msg string) {
	if err != nil {
		log.Fatalf("%s: %v", msg, err)
	}
}

func Filter(vs []string, f func(string) bool) []string {
	vsf := make([]string, 0)
	for _, v := range vs {
		if f(v) {
			vsf = append(vsf, v)
		}
	}
	return vsf
}

func getLastRing(cfg *Config) (string, string, error) {
	fp, err := os.Open(cfg.RingDir)
	if err != nil {
		return "", "", err
	}
	names, err := fp.Readdirnames(-1)
	fp.Close()
	if err != nil {
		return "", "", err
	}

	var lastBuilder string
	fn := Filter(names, func(v string) bool {
		return strings.HasSuffix(v, "-ort.builder")
	})
	sort.Strings(fn)
	if len(fn) != 0 {
		lastBuilder = filepath.Join(cfg.RingDir, fn[len(fn)-1])
	} else {
		_, err := os.Stat(filepath.Join(cfg.RingDir, "ort.builder"))
		if err != nil {
			return "", "", fmt.Errorf("No builder file found in %s", cfg.RingDir)
		}
		lastBuilder = filepath.Join(cfg.RingDir, "ort.builder")
	}
	log.Printf("Found %s, as last builder", lastBuilder)

	var lastRing string
	fn = Filter(names, func(v string) bool {
		return strings.HasSuffix(v, "-ort.ring")
	})
	if len(fn) != 0 {
		lastRing = filepath.Join(cfg.RingDir, fn[len(fn)-1])
	} else {
		_, err := os.Stat(filepath.Join(cfg.RingDir, "ort.ring"))
		if err != nil {
			return "", "", fmt.Errorf("No ring file found in %s", cfg.RingDir)
		}
		lastRing = filepath.Join(cfg.RingDir, "ort.ring")
	}
	log.Printf("Found %s, as last ring", lastRing)
	return lastBuilder, lastRing, nil
}

func newRingMgrServer(cfg *Config) (*ringmgr, error) {
	var err error
	s := new(ringmgr)
	s.cfg = cfg

	bfile, rfile, err := getLastRing(cfg)
	if err != nil {
		panic(err)
	}
	_, s.b, err = ring.RingOrBuilder(bfile)
	FatalIf(err, "Builder file load")
	s.r, _, err = ring.RingOrBuilder(rfile)
	FatalIf(err, "Ring file load")
	s.version = s.r.Version()
	log.Println("Ring version is:", s.version)
	s.rb, s.bb, err = s.loadRingBuilderBytes(s.version)
	FatalIf(err, "Attempting to load ring/builder bytes")

	for _, v := range cfg.NetFilter {
		_, n, err := net.ParseCIDR(v)
		if err != nil {
			FatalIf(err, "Invalid network range provided")
		}
		s.netlimits = append(s.netlimits, n)
	}
	s.tierlimits = cfg.TierFilter

	s.slaves = cfg.Slaves

	if len(s.slaves) == 0 {
		log.Println("!! Running without slaves, have no one to register !!")
		return s, nil
	}

	failcount := 0
	for _, slave := range s.slaves {
		if err = s.RegisterSlave(slave); err != nil {
			log.Println("Got error:", err)
			failcount++
		}
	}
	if failcount > (len(s.slaves) / 2) {
		log.Fatalln("More than half of the ring slaves failed to respond. Exiting.")
	}
	return s, nil
}

func newRingDistServer() *ringslave {
	s := new(ringslave)
	return s
}

func main() {

	cfg, err := loadConfig("/etc/ort/syndicate.toml")
	if err != nil {
		log.Println(err)
		return
	}
	if cfg.Master {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		FatalIf(err, "Failed to bind to port")
		var opts []grpc.ServerOption
		if cfg.UseTLS {
			creds, err := credentials.NewServerTLSFromFile(cfg.CertFile, cfg.KeyFile)
			FatalIf(err, "Couldn't load cert from file")
			opts = []grpc.ServerOption{grpc.Creds(creds)}
		}
		s := grpc.NewServer(opts...)

		r, err := newRingMgrServer(cfg)
		FatalIf(err, "Couldn't prep ring mgr server")
		pb.RegisterRingMgrServer(s, r)

		log.Printf("Master starting up on %d...\n", cfg.Port)
		s.Serve(l)
	} else {
		l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
		FatalIf(err, "Failed to bind to port")
		var opts []grpc.ServerOption
		if cfg.UseTLS {
			creds, err := credentials.NewServerTLSFromFile(cfg.CertFile, cfg.KeyFile)
			FatalIf(err, "Couldn't load cert from file")
			opts = []grpc.ServerOption{grpc.Creds(creds)}
		}
		s := grpc.NewServer(opts...)

		pb.RegisterRingDistServer(s, newRingDistServer())
		log.Printf("Starting ring slave up on %d...\n", cfg.Port)
		s.Serve(l)
	}
}
