package kvstore

import (
	"container/list"
	"context"
	"go.etcd.io/etcd/clientv3"
	"sync"
	"time"
)

type EtcdClientAllocator interface {
	Get(context.Context) (*clientv3.Client, error)
	Put(*clientv3.Client)
}

func NewRoundRobinEtcdClientAllocator(endpoints []string, timeout time.Duration, capacity, maxUsage int) (EtcdClientAllocator, error) {
	return &roundRobin{
		all:       make(map[*clientv3.Client]*rrEntry),
		full:      make(map[*clientv3.Client]*rrEntry),
		waitList:  list.New(),
		max:       maxUsage,
		capacity:  capacity,
		timeout:   timeout,
		endpoints: endpoints,
	}, nil
}

type rrEntry struct {
	client *clientv3.Client
	count  int
	age    time.Time
}

type roundRobin struct {
	block chan struct{}
	sync.Mutex
	available []*rrEntry
	all       map[*clientv3.Client]*rrEntry
	full      map[*clientv3.Client]*rrEntry
	waitList  *list.List
	max       int
	capacity  int
	timeout   time.Duration
	ageOut    time.Duration
	endpoints []string
	size      int
}

func (r *roundRobin) Get(ctx context.Context) (*clientv3.Client, error) {
	r.Lock()

	// first determine if we need to block, which would mean the
	// available queue is empty and we are at capacity
	if len(r.available) == 0 && r.size >= r.capacity {

		// create a channel on which to wait and
		// add it to the list
		ch := make(chan struct{})
		element := r.waitList.PushBack(ch)
		r.Unlock()

		// block until it is our turn or context
		// expires or is canceled
		select {
		case <-ch:
			r.waitList.Remove(element)
		case <-ctx.Done():
			r.waitList.Remove(element)
			return nil, ctx.Err()
		}
		r.Lock()
	}

	defer r.Unlock()
	if len(r.available) > 0 {
		// pull off back end as it is operationally quicker
		last := len(r.available) - 1
		entry := r.available[last]
		entry.count++
		if entry.count >= r.max {
			r.available = r.available[:last]
			r.full[entry.client] = entry
		}
		entry.age = time.Now()
		return entry.client, nil
	}

	// increase capacity
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   r.endpoints,
		DialTimeout: r.timeout,
	})
	if err != nil {
		return nil, err
	}
	entry := &rrEntry{
		client: client,
		count:  1,
	}
	r.all[entry.client] = entry

	if r.max > 1 {
		r.available = append(r.available, entry)
	} else {
		r.full[entry.client] = entry
	}
	r.size++
	return client, nil
}

func (r *roundRobin) Put(client *clientv3.Client) {
	r.Lock()
	entry := r.all[client]
	entry.count--

	// This entry is now available for use, so
	// if in full map add it to available and
	// remove from full
	if _, ok := r.full[client]; ok {
		r.available = append(r.available, entry)
		delete(r.full, client)
	}

	front := r.waitList.Front()
	if front != nil {
		ch := r.waitList.Remove(front)
		r.Unlock()
		// need to unblock if someone is waiting
		ch.(chan struct{}) <- struct{}{}
		return
	}
	r.Unlock()
}
