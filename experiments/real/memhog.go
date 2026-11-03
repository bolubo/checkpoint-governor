// Command memhog is a test process, not a product: it commits anonymous pages in 100 MiB chunks and
// holds them, so a container's memory.max can be driven to its eviction line. The cgroup limit is
// what makes it safe; never run it without one.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	mb, err := strconv.Atoi(os.Args[1])
	if err != nil {
		panic(err)
	}
	var blocks [][]byte
	for i := 0; i < mb/100; i++ {
		b := make([]byte, 100<<20)
		for j := range b {
			b[j] = byte(j)
		}
		blocks = append(blocks, b)
		fmt.Printf("held %d MiB\n", (i+1)*100)
		time.Sleep(100 * time.Millisecond)
	}
	if rest := mb % 100; rest > 0 {
		b := make([]byte, rest<<20)
		for j := range b {
			b[j] = byte(j)
		}
		blocks = append(blocks, b)
	}
	fmt.Printf("holding %d MiB\n", mb)
	for {
		time.Sleep(time.Hour)
	}
}
