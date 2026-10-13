package remote

import (
	"sync"

	"github.com/m-manu/rsync-sidekick/v2/fmte"
	"github.com/m-manu/rsync-sidekick/v2/lib"
	"github.com/m-manu/rsync-sidekick/v2/service"
)

var (
	agentDigestCacheMu    sync.Mutex
	agentDigestCacheTried bool
)

// useAgentDigestCache sets up the agent's cache on the first digest request. Without a
// spec, as from older clients or without --digest-cache, digests are still remembered
// for the lifetime of the agent, just not saved.
func useAgentDigestCache(spec *DigestCacheSpec) {
	agentDigestCacheMu.Lock()
	defer agentDigestCacheMu.Unlock()
	if agentDigestCacheTried {
		return
	}
	agentDigestCacheTried = true
	if spec == nil {
		service.SetDigestCache(service.NewMemoryDigestCache())
		return
	}
	path := spec.Path
	if path == "" {
		defaultPath, err := service.DefaultDigestCachePath()
		if err != nil {
			fmte.PrintfErr("warning: remote: no default location for the digest cache (%+v) - digests are not saved\n", err)
			service.SetDigestCache(service.NewMemoryDigestCache())
			return
		}
		path = defaultPath
	}
	c, err := service.OpenDigestCache(path, spec.Roots)
	if err != nil {
		fmte.PrintfErr("warning: remote: %+v - digests are not saved\n", err)
		service.SetDigestCache(service.NewMemoryDigestCache())
		return
	}
	service.SetDigestCache(c)
}

func closeAgentDigestCache() {
	agentDigestCacheMu.Lock()
	defer agentDigestCacheMu.Unlock()
	agentDigestCacheTried = false
	c := service.ActiveDigestCache()
	if c == nil {
		return
	}
	service.SetDigestCache(nil)
	if !c.Persistent() {
		return
	}
	hits, misses := c.Stats()
	fmte.PrintfErr("Remote digest cache %s: %s reused, %s computed\n", c.Path(),
		lib.GroupThousands(hits), lib.GroupThousands(misses))
	if err := c.Close(); err != nil {
		fmte.PrintfErr("warning: remote: couldn't save digest cache %s: %+v\n", c.Path(), err)
	}
}
