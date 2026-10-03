package fs

import "sync"

// walkParallel walks a directory tree with threads workers that share one queue of
// directories. visit reads one directory and returns its entries plus the subdirectories
// still to walk. Each worker collects its entries on its own; a worker that finds the
// queue empty waits as long as another one may still add subdirectories. With emit set,
// the entries of each directory go to emit right away instead (from several workers at
// once) and nothing is returned.
//
// Several outstanding requests keep all disks of an array busy and let the I/O scheduler
// sort the seeks. The order of the result is not defined.
func walkParallel[D any](root D, threads int, visit func(D) ([]DirEntry, []D), emit func([]DirEntry)) []DirEntry {
	threads = max(threads, 1)
	var mu sync.Mutex
	queueChanged := sync.NewCond(&mu)
	queue := []D{root}
	busy := 0
	perWorker := make([][]DirEntry, threads)
	var wg sync.WaitGroup
	wg.Add(threads)
	for w := 0; w < threads; w++ {
		go func(w int) {
			defer wg.Done()
			for {
				mu.Lock()
				for len(queue) == 0 && busy > 0 {
					queueChanged.Wait()
				}
				if len(queue) == 0 {
					mu.Unlock()
					return
				}
				dir := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				busy++
				mu.Unlock()

				entries, subdirs := visit(dir)
				if emit != nil {
					if len(entries) > 0 {
						emit(entries)
					}
				} else {
					perWorker[w] = append(perWorker[w], entries...)
				}

				mu.Lock()
				queue = append(queue, subdirs...)
				busy--
				mu.Unlock()
				queueChanged.Broadcast()
			}
		}(w)
	}
	wg.Wait()

	total := 0
	for _, entries := range perWorker {
		total += len(entries)
	}
	result := make([]DirEntry, 0, total)
	for _, entries := range perWorker {
		result = append(result, entries...)
	}
	return result
}
