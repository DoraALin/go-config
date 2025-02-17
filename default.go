package config

import (
	"bytes"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/DoraALin/go-config/reader"
	"github.com/DoraALin/go-config/reader/json"
	"github.com/DoraALin/go-config/source"
)

type config struct {
	exit chan bool
	opts Options

	sync.RWMutex
	// the current merged set
	set *source.ChangeSet
	// the current values
	vals reader.Values
	// all the sets
	sets []*source.ChangeSet
	// all the sources
	sources []source.Source

	idx      int
	watchers map[int]*watcher
}

type watcher struct {
	exit    chan bool
	path    []string
	value   Value
	updates chan Value
}

func newConfig(opts ...Option) Config {
	options := Options{
		Reader: json.NewReader(),
	}

	for _, o := range opts {
		o(&options)
	}

	c := &config{
		exit:     make(chan bool),
		opts:     options,
		watchers: make(map[int]*watcher),
		sources:  options.Source,
	}

	for i, s := range options.Source {
		go c.watch(i, s)
	}
	return c
}

func (c *config) watch(idx int, s source.Source) {
	c.Lock()
	c.sets = append(c.sets, nil)
	c.Unlock()

	// watches a source for changes
	watch := func(idx int, s source.Watcher) error {
		for {
			// get changeset
			cs, err := s.Next()
			if err != nil {
				return err
			}

			c.Lock()

			// save
			c.sets[idx] = cs

			// merge sets
			set, err := c.opts.Reader.Parse(c.sets...)
			if err != nil {
				return err
			}

			// set values
			c.vals, _ = c.opts.Reader.Values(set)
			c.set = set

			c.Unlock()

			// send watch updates
			c.update()
		}
	}

	for {
		// watch the source
		w, err := s.Watch()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}

		done := make(chan bool)

		// the stop watch func
		go func() {
			select {
			case <-done:
			case <-c.exit:
			}
			w.Stop()
		}()

		// block watch
		if err := watch(idx, w); err != nil {
			// do something better
			time.Sleep(time.Second)
		}

		// close done chan
		close(done)

		// if the config is closed exit
		select {
		case <-c.exit:
			return
		default:
		}
	}
}

func (c *config) loaded() bool {
	var loaded bool
	c.RLock()
	if c.vals != nil {
		loaded = true
	}
	c.RUnlock()
	return loaded
}

func (c *config) update() {
	var watchers []*watcher

	c.RLock()
	for _, w := range c.watchers {
		watchers = append(watchers, w)
	}
	c.RUnlock()

	for _, w := range watchers {
		select {
		case w.updates <- c.vals.Get(w.path...):
		default:
		}
	}
}

// sync loads all the sources, calls the parser and updates the config
func (c *config) sync() {
	var sets []*source.ChangeSet

	c.Lock()

	// read the source
	for _, source := range c.sources {
		ch, err := source.Read()
		if err != nil {
			continue
		}
		sets = append(sets, ch)
	}

	// merge sets
	set, err := c.opts.Reader.Parse(sets...)
	if err != nil {
		return
	}

	// set values
	c.vals, _ = c.opts.Reader.Values(set)
	c.set = set

	c.Unlock()

	// update watchers
	c.update()
}

// reload reads the sets and creates new values
func (c *config) reload() {
	c.Lock()

	// merge sets
	set, err := c.opts.Reader.Parse(c.sets...)
	if err != nil {
		c.Unlock()
		return
	}

	// set values
	c.vals, _ = c.opts.Reader.Values(set)
	c.set = set

	c.Unlock()

	// update watchers
	c.update()
}

func (c *config) Close() error {
	select {
	case <-c.exit:
		return nil
	default:
		close(c.exit)
	}
	return nil
}

func (c *config) Get(path ...string) Value {
	if !c.loaded() {
		c.sync()
	}

	c.Lock()
	defer c.Unlock()

	// did sync actually work?
	if c.vals != nil {
		return c.vals.Get(path...)
	}

	ch := c.set

	// we are truly screwed, trying to load in a hacked way
	v, err := c.opts.Reader.Values(ch)
	if err != nil {
		log.Printf("Failed to read values %v trying again", err)
		// man we're so screwed
		// Let's try hack this
		// We should really be better
		if ch == nil || ch.Data == nil {
			ch = &source.ChangeSet{
				Timestamp: time.Now(),
				Source:    "config",
				Data:      []byte(`{}`),
			}
		}
		v, _ = c.opts.Reader.Values(ch)
	}

	// lets set it just because
	c.vals = v

	if c.vals != nil {
		return c.vals.Get(path...)
	}

	// ok we're going hardcore now
	return newValue()
}

func (c *config) Bytes() []byte {
	if !c.loaded() {
		c.sync()
	}

	c.Lock()
	defer c.Unlock()

	if c.vals == nil {
		return []byte{}
	}

	return c.vals.Bytes()
}

func (c *config) Load(sources ...source.Source) error {
	for _, source := range sources {
		set, _ := source.Read()
		c.Lock()
		c.sources = append(c.sources, source)
		c.sets = append(c.sets, set)
		idx := len(c.sets) - 1
		c.Unlock()
		go c.watch(idx, source)
	}

	c.reload()
	return nil
}

func (c *config) Watch(path ...string) (Watcher, error) {
	value := c.Get(path...)

	c.Lock()

	w := &watcher{
		exit:    make(chan bool),
		path:    path,
		value:   value,
		updates: make(chan Value, 1),
	}

	id := c.idx
	c.watchers[id] = w
	c.idx++

	c.Unlock()

	go func() {
		<-w.exit
		c.Lock()
		delete(c.watchers, id)
		c.Unlock()
	}()

	return w, nil
}

func (w *watcher) Next() (Value, error) {
	for {
		select {
		case <-w.exit:
			return nil, errors.New("watcher stopped")
		case v := <-w.updates:
			if bytes.Equal(w.value.Bytes(), v.Bytes()) {
				continue
			}
			w.value = v
			return v, nil
		}
	}
}

func (w *watcher) Stop() error {
	select {
	case <-w.exit:
	default:
		close(w.exit)
	}
	return nil
}
