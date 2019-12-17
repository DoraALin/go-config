package consul

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net"

	"github.com/hashicorp/consul/api"
	"github.com/DoraALin/go-config/source"
)

// Currently a single consul reader
type consul struct {
	prefix string
	addr   string
	opts   source.Options
	client *api.Client
}

var (
	DefaultPrefix = "/micro/config"
)

func (c *consul) Read() (*source.ChangeSet, error) {
	kv, _, err := c.client.KV().List(c.prefix, nil)
	if err != nil {
		return nil, err
	}

	if kv == nil || len(kv) == 0 {
		return nil, fmt.Errorf("source not found: %s", c.prefix)
	}

	data := make(map[string]interface{})

	for _, v := range kv {
		data[v.Key] = v.Value
	}

	b, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("error reading source: %v", err)
	}

	// hash the consul
	h := md5.New()
	h.Write(b)

	return &source.ChangeSet{
		Source:   c.String(),
		Data:     b,
		Checksum: fmt.Sprintf("%x", h.Sum(nil)),
	}, nil
}

func (c *consul) String() string {
	return "consul"
}

func (c *consul) Watch() (source.Watcher, error) {
	w, err := newWatcher(c.prefix, c.addr, c.String())
	if err != nil {
		return nil, err
	}
	return w, nil
}

func NewSource(opts ...source.Option) source.Source {
	var options source.Options

	for _, o := range opts {
		o(&options)
	}

	// use default config
	config := api.DefaultConfig()

	// check if there are any addrs
	if options.Context != nil {
		a, ok := options.Context.Value(addressKey{}).(string)
		if ok {
			addr, port, err := net.SplitHostPort(a)
			if ae, ok := err.(*net.AddrError); ok && ae.Err == "missing port in address" {
				port = "8500"
				addr = a
				config.Address = fmt.Sprintf("%s:%s", addr, port)
			} else if err == nil {
				config.Address = fmt.Sprintf("%s:%s", addr, port)
			}
		}
	}

	// create the client
	client, _ := api.NewClient(config)

	prefix := DefaultPrefix
	if options.Context != nil {
		f, ok := options.Context.Value(prefixKey{}).(string)
		if ok {
			prefix = f
		}
	}

	return &consul{
		prefix: prefix,
		addr:   config.Address,
		opts:   options,
		client: client,
	}
}
