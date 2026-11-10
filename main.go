// nitrous is a proof-of-concept external ESP for Minecraft: it attaches
// read-only to a running client and draws the hitboxes of the players around
// you over the game window. It never writes to or injects into the game.
//
// There is nothing to configure: start Minecraft, join a world, run nitrous.
package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"nitrous/internal/hotspot"
	"nitrous/internal/mc"
	"nitrous/internal/overlay"
	"nitrous/internal/proc"
)

// Fallback pacing when the compositor can't be waited on.
const frameInterval = 5 * time.Millisecond

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	p, client, err := attach()
	if err != nil {
		return err
	}
	defer p.Close()
	fmt.Printf("attached to pid %d (Minecraft class %q)\n", p.PID, client.ClassName())

	o, err := overlay.Start(p.PID)
	if err != nil {
		return err
	}
	for p.Alive() {
		// A failed sample is skipped: the overlay keeps showing the previous one.
		if w, err := client.Sample(); err == nil {
			o.Set(scene(w))
		}
		// One sample per display refresh, taken right after the compositor
		// presents, so every update reaches the screen with equal latency.
		if !overlay.WaitFrame() {
			time.Sleep(frameInterval)
		}
	}
	fmt.Println("Minecraft exited")
	return nil
}

// scene is the ESP: every nearby player's hitbox, colored by distance.
func scene(w *mc.World) overlay.Scene {
	var s overlay.Scene
	if w.View == nil {
		return s // not in a world, or the camera is unreadable: nowhere to project to
	}
	s.View = w.View
	for _, p := range w.Players {
		if p.HasPos {
			s.Box(p.Bounds(w.Partial), distanceColor(p.Distance))
		}
	}
	return s
}

// Minecraft's chat colors.
var (
	gray   = overlay.RGB(170, 170, 170)
	green  = overlay.RGB(85, 255, 85)
	yellow = overlay.RGB(255, 255, 85)
	white  = overlay.RGB(255, 255, 255)
)

// distanceColor is the color for a player d blocks away (+Inf = unknown).
func distanceColor(d float64) overlay.Color {
	switch {
	case math.IsInf(d, 1):
		return gray
	case d <= 16:
		return green
	case d <= 48:
		return yellow
	}
	return white
}

// attach finds the Minecraft client: the first java process that turns out
// to be one.
func attach() (*proc.Process, *mc.Client, error) {
	list, err := proc.List()
	if err != nil {
		return nil, nil, err
	}
	var errs []error
	for _, pi := range list {
		if n := strings.ToLower(pi.Name); n != "javaw.exe" && n != "java.exe" {
			continue
		}
		p, client, err := tryAttach(pi.PID)
		if err == nil {
			return p, client, nil
		}
		errs = append(errs, fmt.Errorf("pid %d: %w", pi.PID, err))
	}
	if len(errs) == 0 {
		return nil, nil, errors.New("no java.exe/javaw.exe running; is Minecraft open?")
	}
	return nil, nil, errors.Join(errs...)
}

func tryAttach(pid uint32) (*proc.Process, *mc.Client, error) {
	p, err := proc.Open(pid)
	if err != nil {
		return nil, nil, err
	}
	vm, err := hotspot.Attach(p)
	if err == nil {
		var client *mc.Client
		if client, err = mc.Attach(vm); err == nil {
			return p, client, nil
		}
	}
	p.Close()
	return nil, nil, err
}
