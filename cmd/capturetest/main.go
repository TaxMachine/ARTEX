// capturetest drives the machine capture service against a registered machine
// using a task's SSH credential — manual verification of the pktmon/tcpdump
// paths outside a worker run. Run inside the artex container:
//
//	capturetest -task 9 -machine office-vm -seconds 20
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/TaxMachine/ARTEX/db"
	"github.com/TaxMachine/ARTEX/intercept"
	"github.com/TaxMachine/ARTEX/machine"
)

func main() {
	taskID := flag.Int64("task", 0, "task id whose credential is used")
	name := flag.String("machine", "", "machine name")
	secs := flag.Int("seconds", 10, "capture duration")
	flag.Parse()
	if *taskID == 0 || *name == "" {
		fmt.Println("need -task and -machine")
		os.Exit(2)
	}
	pg, err := db.Open(os.Getenv("ARTEX_PG_DSN"))
	if err != nil {
		fmt.Println("pg:", err)
		os.Exit(1)
	}
	if err := pg.InitSecrets(os.Getenv("ARTEX_JWT_KEY_DIR")); err != nil {
		fmt.Println("secrets:", err)
		os.Exit(1)
	}
	svc := machine.New(pg, "/app/data")
	scope := intercept.TargetScope{TaskID: strconv.FormatInt(*taskID, 10), WaitBound: 1}
	ctx := intercept.WithTargetScope(context.Background(), scope)
	cap, err := svc.CaptureStart(ctx, *taskID, *name, "", "", "", 0)
	fmt.Printf("start: id=%d err=%v\n", cap.ID, err)
	if err != nil {
		os.Exit(1)
	}
	time.Sleep(time.Duration(*secs) * time.Second)
	stopped, err := svc.CaptureStop(ctx, *taskID, *name, cap.ID, "", 0)
	fmt.Printf("stop: bytes=%d local=%s status=%s err=%v\n", stopped.Bytes, stopped.LocalPath, stopped.Status, err)
}
