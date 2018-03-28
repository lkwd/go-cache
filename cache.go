package cache

import (
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"time"
)

type Item struct {
	Object     interface{}
	Expiration int64
	Accessed   int64
}

// Returns true if the item has expired.
func (item Item) Expired() bool {
	if item.Expiration == 0 {
		return false
	}
	return time.Now().UnixNano() > item.Expiration
}

// Return the time at which this item was last accessed.
func (item Item) LastAccessed() time.Time {
	return time.Unix(0, item.Accessed)
}

const (
	// For use with functions that take an expiration time.
	NoExpiration time.Duration = -1
	// For use with functions that take an expiration time. Equivalent to
	// passing in the same expiration duration as was given to New() or
	// NewFrom() when the cache was created (e.g. 5 minutes.)
	DefaultExpiration time.Duration = 0
)

type Cache struct {
	*cache
	// If this is confusing, see the comment at the bottom of New()
}

type cache struct {
	items   sync.Map
	mu      sync.RWMutex
	janitor *janitor
	*CacheOptions
}

// Add an item to the cache, replacing any existing item. If the duration is 0
// (DefaultExpiration), the cache's default expiration time is used. If it is -1
// (NoExpiration), the item never expires.
func (c *cache) Set(k string, x interface{}, d time.Duration) {
	// "Inlining" of set
	var (
		now time.Time
		e   int64
	)
	if d == DefaultExpiration {
		d = c.Expiration
	}
	if d > 0 {
		now = time.Now()
		e = now.Add(d).UnixNano()
	}
	if c.CacheSize > 0 {
		if d <= 0 {
			// d <= 0 means we didn't set now above
			now = time.Now()
		}
		c.items.Store(k, Item{
			Object:     x,
			Expiration: e,
			Accessed:   now.UnixNano(),
		})
		// TODO: Calls to mu.Unlock are currently not deferred because
		// defer adds ~200 ns (as of go1.)
	} else {
		c.items.Store(k, Item{
			Object:     x,
			Expiration: e,
		})
	}
}

func (c *cache) SetMulti(items map[string]interface{}, d time.Duration) {
	// "Inlining" of set
	var (
		now time.Time
		e   int64
	)
	if d == DefaultExpiration {
		d = c.Expiration
	}
	if d > 0 {
		now = time.Now()
		e = now.Add(d).UnixNano()
	}
	if c.CacheSize > 0 {
		if d <= 0 {
			// d <= 0 means we didn't set now above
			now = time.Now()
		}

		for k, v := range items {
			c.items.Store(k, Item{
				Object:     v,
				Expiration: e,
				Accessed:   now.UnixNano(),
			})
		}
		// TODO: Calls to mu.Unlock are currently not deferred because
		// defer adds ~200 ns (as of go1.)

	} else {

		for k, v := range items {
			c.items.Store(k, Item{
				Object:     v,
				Expiration: e,
			})
		}
	}
}

func (c *cache) set(k string, x interface{}, d time.Duration) {
	var (
		now time.Time
		e   int64
	)
	if d == DefaultExpiration {
		d = c.Expiration
	}
	if d > 0 {
		now = time.Now()
		e = now.Add(d).UnixNano()
	}
	if c.CacheSize > 0 {
		if d <= 0 {
			// d <= 0 means we didn't set now above
			now = time.Now()
		}
		c.items.Store(k, Item{
			Object:     x,
			Expiration: e,
			Accessed:   now.UnixNano(),
		})
	} else {
		c.items.Store(k, Item{
			Object:     x,
			Expiration: e,
		})
	}
}

// Add an item to the cache, replacing any existing item, using the default
// expiration.
func (c *cache) SetDefault(k string, x interface{}) {
	c.Set(k, x, DefaultExpiration)
}

// Add an item to the cache only if an item doesn't already exist for the given
// key, or if the existing item has expired. Returns an error otherwise.
func (c *cache) Add(k string, x interface{}, d time.Duration) error {
	_, found := c.getItem(k)
	if found {
		return fmt.Errorf("Item %s already exists", k)
	}
	c.set(k, x, d)
	return nil
}

// Set a new value for the cache key only if it already exists, and the existing
// item hasn't expired. Returns an error otherwise.
func (c *cache) Replace(k string, x interface{}, d time.Duration) error {
	_, found := c.get(k)
	if !found {
		return fmt.Errorf("Item %s doesn't exist", k)
	}
	c.set(k, x, d)
	return nil
}

// Get an item from the cache. Returns the item or nil, and a bool indicating
// whether the key was found.
func (c *cache) Get(k string) (interface{}, bool) {
	// "Inlining" of get and Expired
	item, found := c.getItem(k)
	if !found {
		return nil, false
	}
	var now int64
	if item.Expiration > 0 {
		now = time.Now().UnixNano()
		if now > item.Expiration {
			return nil, false
		}
	}
	if c.CacheSize > 0 {
		if now == 0 {
			now = time.Now().UnixNano()
		}
		item.Accessed = now
		c.items.Store(k, item)
	}
	return item.Object, true
}

// If LRU functionality is being used (and get implies updating item.Accessed,)
// this function must be write-locked.
func (c *cache) get(k string) (interface{}, bool) {
	item, found := c.getItem(k)
	if !found {
		return nil, false
	}
	// "Inlining" of Expired
	var now int64
	if item.Expiration > 0 {
		now = time.Now().UnixNano()
		if now > item.Expiration {
			return nil, false
		}
	}
	if c.CacheSize > 0 {
		if now == 0 {
			now = time.Now().UnixNano()
		}
		item.Accessed = now
		c.items.Store(k, item)
	}
	return item.Object, true
}

func (c *cache) getItem(k string) (Item, bool) {
	tmp, found := c.items.Load(k)
	if !found {
		return Item{}, false
	}
	return tmp.(Item), true
}

// GetWithExpiration returns an item and its expiration time from the cache.
// It returns the item or nil, the expiration time if one is set (if the item
// never expires a zero value for time.Time is returned), and a bool indicating
// whether the key was found.
func (c *cache) GetWithExpiration(k string) (interface{}, time.Time, bool) {
	// "Inlining" of get and Expired
	item, found := c.getItem(k)
	if !found {
		return nil, time.Time{}, false
	}
	var now int64
	if item.Expiration > 0 {
		now = time.Now().UnixNano()
		if now > item.Expiration {
			return nil, time.Time{}, false
		}
		if c.CacheSize > 0 {
			if now == 0 {
				now = time.Now().UnixNano()
			}
			item.Accessed = now
			c.items.Store(k, item)
		}

		return item.Object, time.Unix(0, item.Expiration), true
	}
	if c.CacheSize > 0 {
		if now == 0 {
			now = time.Now().UnixNano()
		}
		item.Accessed = now
		c.items.Store(k, item)
	}

	// If expiration <= 0 (i.e. no expiration time set) then return the item
	// and a zeroed time.Time
	return item.Object, time.Time{}, true
}

// Increment an item of type int, int8, int16, int32, int64, uintptr, uint,
// uint8, uint32, or uint64, float32 or float64 by n. Returns an error if the
// item's value is not an integer, if it was not found, or if it is not
// possible to increment it by n. To retrieve the incremented value, use one
// of the specialized methods, e.g. IncrementInt64.
func (c *cache) Increment(k string, n int64) error {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		c.mu.Unlock()
		return fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	switch v.Object.(type) {
	case int:
		v.Object = v.Object.(int) + int(n)
	case int8:
		v.Object = v.Object.(int8) + int8(n)
	case int16:
		v.Object = v.Object.(int16) + int16(n)
	case int32:
		v.Object = v.Object.(int32) + int32(n)
	case int64:
		v.Object = v.Object.(int64) + n
	case uint:
		v.Object = v.Object.(uint) + uint(n)
	case uintptr:
		v.Object = v.Object.(uintptr) + uintptr(n)
	case uint8:
		v.Object = v.Object.(uint8) + uint8(n)
	case uint16:
		v.Object = v.Object.(uint16) + uint16(n)
	case uint32:
		v.Object = v.Object.(uint32) + uint32(n)
	case uint64:
		v.Object = v.Object.(uint64) + uint64(n)
	case float32:
		v.Object = v.Object.(float32) + float32(n)
	case float64:
		v.Object = v.Object.(float64) + float64(n)
	default:
		return fmt.Errorf("The value for %s is not an integer", k)
	}
	c.items.Store(k, v)
	return nil
}

// Increment an item of type float32 or float64 by n. Returns an error if the
// item's value is not floating point, if it was not found, or if it is not
// possible to increment it by n. Pass a negative number to decrement the
// value. To retrieve the incremented value, use one of the specialized methods,
// e.g. IncrementFloat64.
func (c *cache) IncrementFloat(k string, n float64) error {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	switch v.Object.(type) {
	case float32:
		v.Object = v.Object.(float32) + float32(n)
	case float64:
		v.Object = v.Object.(float64) + n
	default:
		return fmt.Errorf("The value for %s does not have type float32 or float64", k)
	}
	c.items.Store(k, v)
	return nil
}

// Increment an item of type int by n. Returns an error if the item's value is
// not an int, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt(k string, n int) (int, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type int8 by n. Returns an error if the item's value is
// not an int8, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt8(k string, n int8) (int8, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int8)
	if !ok {
		c.mu.Unlock()
		return 0, fmt.Errorf("The value for %s is not an int8", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type int16 by n. Returns an error if the item's value is
// not an int16, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt16(k string, n int16) (int16, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int16)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int16", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type int32 by n. Returns an error if the item's value is
// not an int32, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt32(k string, n int32) (int32, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int32)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int32", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type int64 by n. Returns an error if the item's value is
// not an int64, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementInt64(k string, n int64) (int64, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int64)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int64", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type uint by n. Returns an error if the item's value is
// not an uint, or if it was not found. If there is no error, the incremented
// value is returned.
func (c *cache) IncrementUint(k string, n uint) (uint, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type uintptr by n. Returns an error if the item's value
// is not an uintptr, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUintptr(k string, n uintptr) (uintptr, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uintptr)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uintptr", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type uint8 by n. Returns an error if the item's value
// is not an uint8, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint8(k string, n uint8) (uint8, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint8)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint8", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type uint16 by n. Returns an error if the item's value
// is not an uint16, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint16(k string, n uint16) (uint16, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint16)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint16", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type uint32 by n. Returns an error if the item's value
// is not an uint32, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint32(k string, n uint32) (uint32, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint32)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint32", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type uint64 by n. Returns an error if the item's value
// is not an uint64, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementUint64(k string, n uint64) (uint64, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint64)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint64", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type float32 by n. Returns an error if the item's value
// is not an float32, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementFloat32(k string, n float32) (float32, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(float32)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an float32", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Increment an item of type float64 by n. Returns an error if the item's value
// is not an float64, or if it was not found. If there is no error, the
// incremented value is returned.
func (c *cache) IncrementFloat64(k string, n float64) (float64, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(float64)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an float64", k)
	}
	nv := rv + n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type int, int8, int16, int32, int64, uintptr, uint,
// uint8, uint32, or uint64, float32 or float64 by n. Returns an error if the
// item's value is not an integer, if it was not found, or if it is not
// possible to decrement it by n. To retrieve the decremented value, use one
// of the specialized methods, e.g. DecrementInt64.
func (c *cache) Decrement(k string, n int64) error {
	// TODO: Implement Increment and Decrement more cleanly.
	// (Cannot do Increment(k, n*-1) for uints.)
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return fmt.Errorf("Item not found")
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	switch v.Object.(type) {
	case int:
		v.Object = v.Object.(int) - int(n)
	case int8:
		v.Object = v.Object.(int8) - int8(n)
	case int16:
		v.Object = v.Object.(int16) - int16(n)
	case int32:
		v.Object = v.Object.(int32) - int32(n)
	case int64:
		v.Object = v.Object.(int64) - n
	case uint:
		v.Object = v.Object.(uint) - uint(n)
	case uintptr:
		v.Object = v.Object.(uintptr) - uintptr(n)
	case uint8:
		v.Object = v.Object.(uint8) - uint8(n)
	case uint16:
		v.Object = v.Object.(uint16) - uint16(n)
	case uint32:
		v.Object = v.Object.(uint32) - uint32(n)
	case uint64:
		v.Object = v.Object.(uint64) - uint64(n)
	case float32:
		v.Object = v.Object.(float32) - float32(n)
	case float64:
		v.Object = v.Object.(float64) - float64(n)
	default:
		return fmt.Errorf("The value for %s is not an integer", k)
	}
	c.items.Store(k, v)
	return nil
}

// Decrement an item of type float32 or float64 by n. Returns an error if the
// item's value is not floating point, if it was not found, or if it is not
// possible to decrement it by n. Pass a negative number to decrement the
// value. To retrieve the decremented value, use one of the specialized methods,
// e.g. DecrementFloat64.
func (c *cache) DecrementFloat(k string, n float64) error {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	switch v.Object.(type) {
	case float32:
		v.Object = v.Object.(float32) - float32(n)
	case float64:
		v.Object = v.Object.(float64) - n
	default:
		return fmt.Errorf("The value for %s does not have type float32 or float64", k)
	}
	c.items.Store(k, v)
	return nil
}

// Decrement an item of type int by n. Returns an error if the item's value is
// not an int, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt(k string, n int) (int, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		c.mu.Unlock()
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type int8 by n. Returns an error if the item's value is
// not an int8, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt8(k string, n int8) (int8, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int8)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int8", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type int16 by n. Returns an error if the item's value is
// not an int16, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt16(k string, n int16) (int16, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int16)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int16", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type int32 by n. Returns an error if the item's value is
// not an int32, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt32(k string, n int32) (int32, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int32)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int32", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type int64 by n. Returns an error if the item's value is
// not an int64, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementInt64(k string, n int64) (int64, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(int64)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an int64", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type uint by n. Returns an error if the item's value is
// not an uint, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementUint(k string, n uint) (uint, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type uintptr by n. Returns an error if the item's value
// is not an uintptr, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUintptr(k string, n uintptr) (uintptr, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uintptr)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uintptr", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type uint8 by n. Returns an error if the item's value is
// not an uint8, or if it was not found. If there is no error, the decremented
// value is returned.
func (c *cache) DecrementUint8(k string, n uint8) (uint8, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint8)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint8", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type uint16 by n. Returns an error if the item's value
// is not an uint16, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint16(k string, n uint16) (uint16, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint16)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint16", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type uint32 by n. Returns an error if the item's value
// is not an uint32, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint32(k string, n uint32) (uint32, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint32)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint32", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type uint64 by n. Returns an error if the item's value
// is not an uint64, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementUint64(k string, n uint64) (uint64, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(uint64)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an uint64", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type float32 by n. Returns an error if the item's value
// is not an float32, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementFloat32(k string, n float32) (float32, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(float32)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an float32", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Decrement an item of type float64 by n. Returns an error if the item's value
// is not an float64, or if it was not found. If there is no error, the
// decremented value is returned.
func (c *cache) DecrementFloat64(k string, n float64) (float64, error) {
	v, found := c.getItem(k)
	if !found || v.Expired() {
		return 0, fmt.Errorf("Item %s not found", k)
	}
	if c.CacheSize > 0 {
		v.Accessed = time.Now().UnixNano()
	}
	rv, ok := v.Object.(float64)
	if !ok {
		return 0, fmt.Errorf("The value for %s is not an float64", k)
	}
	nv := rv - n
	v.Object = nv
	c.items.Store(k, v)
	return nv, nil
}

// Delete an item from the cache. Does nothing if the key is not in the cache.
func (c *cache) Delete(k string) {
	if v, evicted := c.delete(k); evicted {
		c.EvictionCallback(k, v)
	}
}

func (c *cache) delete(k string) (interface{}, bool) {
	if c.EvictionCallback != nil {
		if tmp, found := c.items.Load(k); found {
			v := tmp.(Item)
			c.items.Delete(k)
			return v.Object, true
		}
	}
	c.items.Delete(k)
	return nil, false
}

type keyAndValue struct {
	key   string
	value interface{}
}

// Delete all expired items from the cache.
func (c *cache) DeleteExpired() {
	var evictedItems []keyAndValue
	now := time.Now().UnixNano()
	evictFunc := c.EvictionCallback
	c.items.Range(func(key, value interface{}) bool {

		v := value.(Item)
		k := key.(string)
		// "Inlining" of Expired
		if v.Expiration > 0 && now > v.Expiration {
			ov, evicted := c.delete(k)
			if evicted {
				evictedItems = append(evictedItems, keyAndValue{k, ov})
			}
		}

		return true
	})
	for _, v := range evictedItems {
		evictFunc(v.key, v.value)
	}
}

// Delete some of the oldest items in the cache if the soft size limit has been
// exceeded.
func (c *cache) DeleteLRU() {
	var (
		overCount = c.itemCount() - c.CacheSize
		evictFunc = c.EvictionCallback
	)
	evicted := c.deleteLRUAmount(overCount)
	for _, v := range evicted {
		evictFunc(v.key, v.value)
	}
}

// Delete a number of the oldest items from the cache.
func (c *cache) DeleteLRUAmount(numItems int) {
	c.mu.Lock()
	evictFunc := c.EvictionCallback
	evicted := c.deleteLRUAmount(numItems)
	c.mu.Unlock()
	for _, v := range evicted {
		evictFunc(v.key, v.value)
	}
}

func (c *cache) deleteLRUAmount(numItems int) []keyAndValue {
	if numItems <= 0 {
		return nil
	}
	var (
		lastTime     int64 = 0
		lastItems          = make([]string, numItems) // Ring buffer
		liCount            = 0
		full               = false
		evictedItems []keyAndValue
		now          = time.Now().UnixNano()
	)
	if c.EvictionCallback != nil {
		evictedItems = make([]keyAndValue, 0, numItems)
	}
	c.items.Range(func(key, value interface{}) bool {

		v := value.(Item)
		k := key.(string)

		// "Inlining" of !Expired
		if v.Expiration == 0 || now <= v.Expiration {
			if full == false || v.Accessed < lastTime {
				// We found a least-recently-used item, or our
				// purge buffer isn't full yet
				lastTime = v.Accessed
				// Append it to the buffer, or start overwriting
				// it
				if liCount < numItems {
					lastItems[liCount] = k
					liCount++
				} else {
					lastItems[0] = k
					liCount = 1
					full = true
				}
			}
		}

		return true
	})

	if lastTime > 0 {
		for _, v := range lastItems {
			if v != "" {
				ov, evicted := c.delete(v)
				if evicted {
					evictedItems = append(evictedItems, keyAndValue{v, ov})
				}
			}
		}
	}
	return evictedItems
}

// Write the cache's items (using Gob) to an io.Writer.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) Save(w io.Writer) (err error) {

	m := make(map[string]Item)
	c.items.Range(func(key, value interface{}) bool {
		v := value.(Item)
		k := key.(string)
		m[k] = v
		return true // if false, Range stops
	})

	enc := gob.NewEncoder(w)
	defer func() {
		if x := recover(); x != nil {
			err = fmt.Errorf("Error registering item types with Gob library")
		}
	}()
	for _, v := range m {
		gob.Register(v.Object)
	}

	err = enc.Encode(m)
	return
}

// Save the cache's items to the given filename, creating the file if it
// doesn't exist, and overwriting it if it does.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) SaveFile(fname string) error {
	fp, err := os.Create(fname)
	if err != nil {
		return err
	}
	err = c.Save(fp)
	if err != nil {
		fp.Close()
		return err
	}
	return fp.Close()
}

// Add (Gob-serialized) cache items from an io.Reader, excluding any items with
// keys that already exist (and haven't expired) in the current cache.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) Load(r io.Reader) error {
	dec := gob.NewDecoder(r)
	items := map[string]Item{}
	err := dec.Decode(&items)
	if err == nil {
		for k, v := range items {
			ov, found := c.getItem(k)
			if !found || ov.Expired() {
				c.items.Store(k, v)
			}
		}
	}
	return err
}

// Load and add cache items from the given filename, excluding any items with
// keys that already exist in the current cache.
//
// NOTE: This method is deprecated in favor of c.Items() and NewFrom() (see the
// documentation for NewFrom().)
func (c *cache) LoadFile(fname string) error {
	fp, err := os.Open(fname)
	if err != nil {
		return err
	}
	err = c.Load(fp)
	if err != nil {
		fp.Close()
		return err
	}
	return fp.Close()
}

// Copies all unexpired items in the cache into a new map and returns it.
func (c *cache) Items() map[string]Item {
	m := make(map[string]Item)
	now := time.Now().UnixNano()
	c.items.Range(func(key, value interface{}) bool {
		v := value.(Item)
		k := key.(string)

		// "Inlining" of Expired
		if v.Expiration > 0 {
			if now > v.Expiration {
				return true
			}
		}
		m[k] = v
		return true
	})
	return m
}

// Returns the number of items in the cache. This may include items that have
// expired, but have not yet been cleaned up.
func (c *cache) ItemCount() int {
	n := 0
	c.items.Range(func(_, _ interface{}) bool {
		n++
		return true
	})
	return n
}

// Returns the number of items in the cache without locking. This may include
// items that have expired, but have not yet been cleaned up. Equivalent to
// len(c.Items()).
func (c *cache) itemCount() int {
	n := c.ItemCount()
	return n
}

// Delete all items from the cache.
func (c *cache) Flush() {
	c.mu.Lock()
	c.items = sync.Map{}
	c.mu.Unlock()
}

type janitor struct {
	Interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *cache) {
	j.stop = make(chan bool)
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
			if c.CacheSize > 0 {
				c.DeleteLRU()
			}
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func stopJanitor(c *Cache) {
	c.janitor.stop <- true
}

func runJanitor(c *cache, ci time.Duration) {
	j := &janitor{
		Interval: ci,
	}
	c.janitor = j
	go j.Run(c)
}

func newunexportedCache(items sync.Map, options *CacheOptions) *cache {
	return &cache{
		items:        items,
		CacheOptions: options,
	}
}

func newCache(items sync.Map, options *CacheOptions) *Cache {

	c := newunexportedCache(items, options)

	// This trick ensures that the janitor goroutine (which--granted it
	// was enabled--is running DeleteExpired on c forever) does not keep
	// the returned C object from being garbage collected. When it is
	// garbage collected, the finalizer stops the janitor goroutine, after
	// which c can be collected.
	C := &Cache{c}

	if options.CleanupInterval > 0 {
		runJanitor(c, options.CleanupInterval)
		runtime.SetFinalizer(C, stopJanitor)
	}
	return C
}

type CacheOptions struct {
	Expiration       time.Duration
	CleanupInterval  time.Duration
	EvictionCallback func(string, interface{})
	CacheSize        int
	InitialItems     map[string]Item
	Shards           int
}

type CacheOption func(*CacheOptions) error

func GetDefaultOptions() *CacheOptions {
	return &CacheOptions{
		// no expiration
		Expiration: NoExpiration,
		// no callback
		EvictionCallback: nil,
		// no cleanup
		CleanupInterval: 0,
		// unlimited
		CacheSize:    0,
		InitialItems: nil,
		Shards:       0,
	}
}

func Expiration(e time.Duration) CacheOption {
	return func(m *CacheOptions) error {
		m.Expiration = e
		return nil
	}
}

func CleanupInterval(e time.Duration) CacheOption {
	return func(m *CacheOptions) error {
		m.CleanupInterval = e
		return nil
	}
}

func EvictionCallback(cb func(string, interface{})) CacheOption {
	return func(m *CacheOptions) error {
		m.EvictionCallback = cb
		return nil
	}
}

func CacheSize(c int) CacheOption {
	return func(m *CacheOptions) error {
		m.CacheSize = c
		return nil
	}
}

func InitialItems(i map[string]Item) CacheOption {
	return func(m *CacheOptions) error {
		m.InitialItems = i
		return nil
	}
}

func (c *Cache) Configure(options ...CacheOption) {

	c.mu.Lock()
	for _, opt := range options {
		opt(c.CacheOptions)
	}

	c.mu.Unlock()
}

// Return a new cache with a given default expiration duration and cleanup
// interval. If the expiration duration is less than one (or NoExpiration),
// the items in the cache never expire (by default), and must be deleted
// manually. If the cleanup interval is less than one, expired items are not
// deleted from the cache before calling c.DeleteExpired().
func New(options ...CacheOption) *Cache {

	opts := GetDefaultOptions()

	for _, opt := range options {
		if err := opt(opts); err != nil {
			return nil
		}
	}

	items := sync.Map{}

	if opts.InitialItems != nil {
		for k, v := range opts.InitialItems {
			items.Store(k, v)
		}
	}

	return newCache(items, opts)
}
