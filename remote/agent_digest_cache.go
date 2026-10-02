package remote

import (
	"sync"

	"github.com/m-manu/rsync-sidekick/v2/fmte"
	"github.com/m-manu/rsync-sidekick/v2/service"
)

var (
	agentDigestCacheMu    sync.Mutex
	agentDigestCacheTried bool
)

func useAgentDigestCache(spec *DigestCacheSpec) {
	if spec == nil {
		return
	}
	agentDigestCacheMu.Lock()
	defer agentDigestCacheMu.Unlock()
	if agentDigestCacheTried {
		return
	}
	agentDigestCacheTried = true
	path := spec.Path
	if path == "" {
		defaultPath, err := service.DefaultDigestCachePath()
		if err != nil {
			fmte.PrintfErr("warning: remote: no default location for the digest cache (%+v) - digests are not cached\n", err)
			return
		}
		path = defaultPath
	}
	c, err := service.OpenDigestCache(path)
	if err != nil {
		fmte.PrintfErr("warning: remote: %+v - digests are not cached\n", err)
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
	hits, misses := c.Stats()
	fmte.PrintfErr("Remote digest cache %s: %d reused, %d computed\n", c.Path(), hits, misses)
	if err := c.Close(); err != nil {
		fmte.PrintfErr("warning: remote: couldn't save digest cache %s: %+v\n", c.Path(), err)
	}
}
