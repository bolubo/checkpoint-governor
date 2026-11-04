// Command memhog is a test process, not a product: it commits anonymous pages in 100 MiB chunks and
// holds them, so a container's memory.max can be driven to its eviction line. The cgroup limit is
// what makes it safe; never run it without one.
//
//	memhog <MiB>                 fill to <MiB>, then hold
//	memhog <MiB> <creep> <ms>    fill to <MiB>, then add <creep> MiB every <ms> until the kernel
//	                             kills it — pressure that stays at the line before the death, so
//	                             the probe's next read can land before the OOM
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func touch(mb int) []byte {
	b := make([]byte, mb<<20)
	for j := range b {
		b[j] = byte(j)
	}
	return b
}

func main() {
	mb, err := strconv.Atoi(os.Args[1])
	if err != nil {
		panic(err)
	}
	creep, creepMS := 0, 0
	if len(os.Args) > 2 {
		if creep, err = strconv.Atoi(os.Args[2]); err != nil {
			panic(err)
		}
	}
	if len(os.Args) > 3 {
		if creepMS, err = strconv.Atoi(os.Args[3]); err != nil {
			panic(err)
		}
	}
	var blocks [][]byte
	for i := 0; i < mb/100; i++ {
		blocks = append(blocks, touch(100))
		fmt.Printf("held %d MiB\n", (i+1)*100)
		time.Sleep(100 * time.Millisecond)
	}
	if rest := mb % 100; rest > 0 {
		blocks = append(blocks, touch(rest))
	}
	fmt.Printf("holding %d MiB\n", mb)
	if creep > 0 && creepMS > 0 {
		for {
			time.Sleep(time.Duration(creepMS) * time.Millisecond)
			blocks = append(blocks, touch(creep))
			fmt.Printf("creeped %d MiB\n", creep)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}
