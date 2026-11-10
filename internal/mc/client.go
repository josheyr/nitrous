// Package mc reads the state of a running Minecraft client: the local
// player, the players around them and the camera.
//
// It does not depend on any one set of mappings (MCP/SRG/Yarn/intermediary/
// obfuscated): player entities are recognised structurally as objects holding
// a com.mojang.authlib.GameProfile, which is never obfuscated. From there:
//
//	Minecraft class  = class with a static field of its own type (singleton)
//	                   and an instance field whose declared type is a player class
//	local player     = that instance field
//	world            = the Minecraft field whose object has a java.util.List
//	                   made up of player entities that contains the local player
//
// The players of a World are those the server is currently streaming to this
// client (i.e. within entity-tracking / render range), which is exactly what
// the client world's player list holds.
//
// Everything is read-only.
package mc

import (
	"errors"
	"sort"
	"strings"
	"time"

	"nitrous/internal/geom"
	"nitrous/internal/hotspot"
)

const (
	gameProfileSig = "Lcom/mojang/authlib/GameProfile;"
	listSig        = "Ljava/util/List;"
	maxPlayers     = 1024
	defaultFOV     = 70
)

// Known names for the Minecraft client class, tried before a full scan.
var knownMinecraftClasses = []string{
	"net/minecraft/client/Minecraft", // Forge (SRG/MCP), NeoForge, Mojang-mapped
	"net/minecraft/class_310",        // Fabric / Quilt (intermediary)
	"ave",                            // vanilla 1.8.9 (obfuscated)
}

// Packages that can't contain the Minecraft class; skipped by the full scan.
var libraryPackages = []string{"java/", "javax/", "jdk/", "sun/", "com/sun/", "com/google/", "io/netty/",
	"org/", "it/unimi/", "com/mojang/", "kotlin/", "com/fasterxml/", "oshi/", "net/java/"}

// Client is an attached Minecraft client.
type Client struct {
	// FOV is the vertical field of view assumed when the game's own can't be
	// read. Set it to the in-game setting.
	FOV float64

	vm *hotspot.VM

	klass    hotspot.Klass // the Minecraft class
	instance hotspot.Field // its static singleton
	self     hotspot.Field // the local player

	// resolved lazily, once in a world
	world, players *hotspot.Field
	pos            *posLayout
	ent            *entLayout
	legacy         *legacyCamera
	modern         *modernCamera
	clock          tickClock

	profiles map[hotspot.Klass]*hotspot.Field // GameProfile field per class; nil = not a player
}

// Attach locates the Minecraft client classes in the target JVM.
func Attach(vm *hotspot.VM) (*Client, error) {
	c := &Client{
		FOV:      defaultFOV,
		vm:       vm,
		profiles: map[hotspot.Klass]*hotspot.Field{},
	}
	if len(vm.FindClass("com/mojang/authlib/GameProfile")) == 0 {
		return nil, errors.New("com/mojang/authlib/GameProfile not loaded: this JVM is not a Minecraft client")
	}
	for _, n := range knownMinecraftClasses {
		for _, k := range vm.FindClass(n) {
			if c.tryMinecraft(k) {
				return c, nil
			}
		}
	}
	// Unknown mappings / custom client: structural scan of every loaded class.
	found := false
	vm.AllClasses(func(name string, k hotspot.Klass) bool {
		for _, p := range libraryPackages {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		found = c.tryMinecraft(k)
		return !found
	})
	if !found {
		return nil, errors.New("could not locate the Minecraft client class (still loading?)")
	}
	return c, nil
}

// tryMinecraft adopts k as the Minecraft class if it has the expected shape.
func (c *Client) tryMinecraft(k hotspot.Klass) bool {
	selfSig := "L" + c.vm.Name(k) + ";"
	var inst, self *hotspot.Field
	for _, f := range c.vm.DeclaredFields(k) {
		switch {
		case f.Static && f.Sig == selfSig && inst == nil:
			inst = &f
		case !f.Static && self == nil && c.isPlayerClass(f.ClassName()):
			self = &f
		}
	}
	if inst == nil || self == nil || c.vm.StaticObj(k, *inst) == 0 {
		return false
	}
	c.klass, c.instance, c.self = k, *inst, *self
	return true
}

// ClassName is the (possibly obfuscated) name of the detected Minecraft class.
func (c *Client) ClassName() string { return c.vm.Name(c.klass) }

// profileField returns the GameProfile field if k is a player entity class.
func (c *Client) profileField(k hotspot.Klass) *hotspot.Field {
	if f, ok := c.profiles[k]; ok {
		return f
	}
	var f *hotspot.Field
	for _, cand := range c.vm.AllFields(k) {
		if !cand.Static && cand.Sig == gameProfileSig {
			f = &cand
			break
		}
	}
	c.profiles[k] = f
	return f
}

func (c *Client) isPlayerClass(name string) bool {
	if name == "" {
		return false
	}
	for _, k := range c.vm.FindClass(name) {
		if c.profileField(k) != nil {
			return true
		}
	}
	return false
}

func (c *Client) isPlayer(o hotspot.Oop) bool {
	return o != 0 && c.profileField(c.vm.KlassOf(o)) != nil
}

// resolveWorld finds which Minecraft field holds the world and which List in
// it is the player list, by looking for a list of players containing self.
func (c *Client) resolveWorld(mc, self hotspot.Oop) bool {
	vm := c.vm
	for _, wf := range vm.AllFields(c.klass) {
		if wf.Static || !wf.IsObject() || wf.Offset == c.self.Offset {
			continue
		}
		w := vm.ObjField(mc, wf)
		if w == 0 {
			continue
		}
		for _, lf := range vm.AllFields(vm.KlassOf(w)) {
			if lf.Static || lf.Sig != listSig {
				continue
			}
			elems, ok := vm.ListElems(vm.ObjField(w, lf), maxPlayers)
			if !ok || len(elems) == 0 {
				continue
			}
			hasSelf, allPlayers := false, true
			for _, e := range elems {
				if e == self {
					hasSelf = true
				}
				if !c.isPlayer(e) {
					allPlayers = false
					break
				}
			}
			if hasSelf && allPlayers {
				c.world, c.players = &wf, &lf
				return true
			}
		}
	}
	return false
}

// World is one sample of the game, taken by Client.Sample.
type World struct {
	// InWorld is false on the title screen, while connecting, etc. The other
	// fields are then empty, but Self and Players are never nil.
	InWorld bool

	// Self is the local player.
	Self *Player
	// Players are the other players the server is streaming to this client,
	// closest first.
	Players []*Player

	// View is the camera to draw with: what the game last rendered, ready for
	// projection. Nil if it could not be read.
	View *geom.View
	// Partial is how far (0..1) the frame being drawn is between the previous
	// client tick and the current one. Entities only move once per tick, so
	// draw them at Player.At(Partial) to match what the game shows.
	Partial float64
}

func emptyWorld() *World {
	return &World{Self: &Player{}, Players: []*Player{}, Partial: 1}
}

// Sample reads the current state of the game. Outside a world it returns an
// empty World with InWorld unset; an error means the read itself failed and
// is worth retrying.
func (c *Client) Sample() (*World, error) {
	vm := c.vm
	w := emptyWorld()
	mc := vm.StaticObj(c.klass, c.instance)
	if mc == 0 {
		return nil, errors.New("minecraft instance is null")
	}
	self := vm.ObjField(mc, c.self)
	if self == 0 || (c.world == nil && !c.resolveWorld(mc, self)) {
		return w, nil
	}
	level := vm.ObjField(mc, *c.world)
	if level == 0 {
		return w, nil
	}
	elems, ok := vm.ListElems(vm.ObjField(level, *c.players), maxPlayers)
	if !ok {
		c.world, c.players = nil, nil // layout changed under us; rediscover next time
		return nil, errors.New("player list unreadable, re-resolving")
	}
	w.InWorld = true

	if c.pos == nil {
		c.pos = c.resolvePos(self)
	}
	if c.ent == nil {
		c.ent = c.resolveEnt(self)
		c.resolveCameras(mc)
	}

	w.Self = c.player(self)
	w.Self.Distance = 0
	c.locate(w.Self)
	if w.Self.HasPos {
		if cam := c.camera(mc, w.Self); cam != nil {
			w.View, w.Partial = c.view(cam, time.Now())
		}
	}

	seen := map[[2]string]bool{}
	for _, e := range elems {
		if e == self || !c.isPlayer(e) {
			continue
		}
		p := c.player(e)
		id := [2]string{p.Name, p.UUID}
		if p.Name == "" || seen[id] {
			continue
		}
		seen[id] = true
		if c.locate(p); p.HasPos && w.Self.HasPos {
			p.Distance = p.Pos.Dist(w.Self.Pos)
		}
		w.Players = append(w.Players, p)
	}
	// closest first; players without a distance (+Inf) go last
	sort.SliceStable(w.Players, func(i, j int) bool { return w.Players[i].Distance < w.Players[j].Distance })
	return w, nil
}
