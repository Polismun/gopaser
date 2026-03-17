package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"

	dem "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs"
	events "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/events"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			outputError(fmt.Sprintf("Parser panic: %v", r))
		}
	}()

	var reader io.Reader
	if len(os.Args) >= 2 {
		// Legacy: file path as argument (local dev / tests)
		f, err := os.Open(os.Args[1])
		if err != nil {
			outputError(fmt.Sprintf("Erreur lors de l'ouverture du fichier: %v", err))
			return
		}
		defer f.Close()
		reader = f
	} else {
		// Streaming mode: read from stdin (piped from HTTP request body)
		reader = os.Stdin
	}

	p := dem.NewParser(reader)

	header, err := p.ParseHeader()
	if err != nil {
		outputError(fmt.Sprintf("Erreur lors du parsing du header: %v", err))
		return
	}

	result := ParseResult{
		Success:       true,
		MapName:       header.MapName,
		ServerName:    header.ServerName,
		TickRate:      int(p.TickRate()),
		Stats:         []PlayerStats{},
		Ticks:         []TickData{},
		Shots:         []ShotEvent{},
		Damages:       []DamageEvent{},
		GrenadeEvents: []GrenadeEvent{},
		Kills:         []KillEvent{},
	}

	// Fallback map name from MatchStart event
	p.RegisterEventHandler(func(e events.MatchStart) {
		if result.MapName == "" || result.MapName == "unknown" {
			if h, err := p.ParseHeader(); err == nil && h.MapName != "" {
				result.MapName = h.MapName
			}
		}
	})

	bs := &bombState{}
	roundNumber := 0
	srs := &skipRoundState{}

	registerFrameHandler(p, &result, bs, srs)
	registerEventHandlers(p, &result, bs, &roundNumber, srs)

	if err := p.ParseToEnd(); err != nil {
		outputError(fmt.Sprintf("Erreur lors du parsing: %v", err))
		return
	}

	// Extract final KDA stats
	for _, player := range p.GameState().Participants().Playing() {
		if player == nil {
			continue
		}
		teamName := "Spectator"
		if player.Team == 2 {
			teamName = "T"
		} else if player.Team == 3 {
			teamName = "CT"
		}
		result.Stats = append(result.Stats, PlayerStats{
			Name:    player.Name,
			Team:    teamName,
			SteamID: player.SteamID64,
			Kills:   player.Kills(),
			Deaths:  player.Deaths(),
			Assists: player.Assists(),
		})
	}

	// Free demoinfocs library state (~7 GB) BEFORE JSON encoding
	p.Close()
	runtime.GC()

	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "Erreur lors de l'encodage JSON: %v\n", err)
		os.Exit(1)
	}
}

func outputError(msg string) {
	json.NewEncoder(os.Stdout).Encode(ParseResult{Success: false, Error: msg})
	os.Exit(1)
}
