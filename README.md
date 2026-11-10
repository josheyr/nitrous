<div align="center">

```
 ███╗   ██╗██╗████████╗██████╗  ██████╗ ██╗   ██╗███████╗
 ████╗  ██║██║╚══██╔══╝██╔══██╗██╔═══██╗██║   ██║██╔════╝
 ██╔██╗ ██║██║   ██║   ██████╔╝██║   ██║██║   ██║███████╗
 ██║╚██╗██║██║   ██║   ██╔══██╗██║   ██║██║   ██║╚════██║
 ██║ ╚████║██║   ██║   ██║  ██║╚██████╔╝╚██████╔╝███████║
 ╚═╝  ╚═══╝╚═╝   ╚═╝   ╚═╝  ╚═╝ ╚═════╝  ╚═════╝ ╚══════╝
```

**A proof-of-concept Minecraft ESP that never enters Minecraft.**

Nitrous reads a running game straight out of the JVM's memory, from a separate process,<br>
and draws what it finds on a transparent window floating over the game.

`Go` · `Windows` · `one dependency` · `read-only`

</div>

---

> [!IMPORTANT]
> **Nitrous is a proof of concept.** It exists to show that a player ESP can be built entirely from outside the game, with nothing but read access to the JVM's memory. It is not a product: there are no options, no releases, no support, and no promise that it works on your client or keeps working. It does one thing, and the interesting part is *how*.

## What it is

Nitrous is an external player ESP for Minecraft. It attaches to the game the way a debugger would, works out where everything lives, and paints over the top: every nearby player's hitbox as a wireframe box, visible through walls, colored by distance (green within 16 blocks, yellow within 48, white beyond).

That is all it does. There are no flags, no settings and no HUD.

There is no injected DLL, no Java agent, no Forge or Fabric mod, no modified jar. The only handle Nitrous ever opens on the game is this one:

```go
windows.OpenProcess(PROCESS_VM_READ|PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
```

It cannot write to the game even if it wanted to.

## Why that is hard

A Minecraft mod normally gets to call `player.getX()`. Nitrous gets a pile of bytes and has to answer three questions on its own.

### 1. Where is anything in this JVM?

HotSpot's internal layouts change with every JDK release, GC choice and command-line flag. Rather than hardcode offsets for each build, Nitrous reads the tables HotSpot exports for its own Serviceability Agent (`gHotSpotVMStructs`, `gHotSpotVMTypes` and the int/long constant tables) and resolves everything at runtime.

That is what lets one binary cope with:

| | |
|---|---|
| **JDK versions** | 8 through 25 |
| **Compressed oops and class pointers** | base and shift read from the live VM |
| **Compact object headers** | narrow klass pulled out of the mark word |
| **Generational ZGC** | colored pointers decoded |
| **Field metadata** | the classic `fields[]` array and the JDK 21+ `fieldinfo_stream` |
| **Array layout** | including the JDK 22 change to small-element alignment |

### 2. Which of these 20,000 classes is the game?

Minecraft ships obfuscated, and every mod loader renames things differently (MCP, SRG, Yarn, intermediary, Mojang mappings). Nitrous depends on none of them. It recognises the game by its *shape*:

```
player entity    =  any object holding a com.mojang.authlib.GameProfile
                    (authlib is a library, so that name is never obfuscated)

Minecraft class  =  a class with a static field of its own type (the singleton)
                    and an instance field whose type is a player class

local player     =  that instance field

world            =  the Minecraft field whose object owns a java.util.List
                    made entirely of players, one of which is you
```

Three well-known names are tried first as a shortcut (`net/minecraft/client/Minecraft`, Fabric's `class_310`, vanilla 1.8.9's `ave`). If none match, every loaded class is checked against the shape above, so custom and unknown clients still resolve.

### 3. Where on screen does a point in the world land?

To draw a box around a player, Nitrous needs the game's camera. It reads the renderer's own state for the frame the game last drew, including the projection and modelview matrices when they are reachable, and falls back to rebuilding the view from position, yaw, pitch and FOV when they are not.

Two details keep the boxes glued to the players instead of swimming around them:

- **Partial ticks.** Entities move 20 times a second, and the game interpolates between ticks when rendering. Nitrous reads the partial tick (or estimates it from the tick counter) and interpolates the same way.
- **Compositor pacing.** The game is sampled once per display refresh, right after the desktop compositor presents (`DwmFlush`), so every update reaches the screen with the same latency.

## The overlay

A transparent, click-through Win32 window that follows the game's client area, drawn with plain GDI into a persistent back buffer.

It works in windowed, borderless and fullscreen without special privileges. Fullscreen game windows are topmost, which would normally bury an overlay, so Nitrous has the game window *own* the overlay window: Windows always keeps an owned window above its owner.

## Running it

Requires Windows and Go 1.27 or newer.

```sh
go build -o nitrous.exe .
```

Start Minecraft, join a world, then:

```sh
nitrous
```

It takes no arguments. It finds the `java.exe` / `javaw.exe` that is actually a Minecraft client by itself, draws while the game has focus, and exits when the game does.

## Layout

```
main.go              attach, sample once per refresh, draw the boxes
internal/
  proc/              read-only process memory (ReadProcessMemory)
  hotspot/           the JVM from outside: vmStructs tables, classes, fields, heap objects
  mc/                the game: client, world, players, camera
  geom/              vectors, boxes, matrices, world-to-screen projection
  overlay/           the transparent Win32 window and its painter
```

Each layer only knows about the ones above it in this list.

```sh
go test ./...
```

## Limits

- **Proof of concept.** No guarantees: on a client whose layout it cannot work out, it simply draws nothing.
- **Windows only.** Process access and the overlay are Win32.
- **Only what the server sends.** The players come from the client world's own list, so it covers players inside entity-tracking range and nothing beyond.
- **Read-only, by construction.** It observes; it cannot change anything in the game.

## Fair play

Seeing players through walls is an advantage that most multiplayer servers prohibit, and being external does not make it permitted. Nitrous is a technical demonstration: run it in single-player, on your own server, or where the rules allow it.
