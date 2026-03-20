package main

import (
	"bufio"
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
		f, err := os.Open(os.Args[1])
		if err != nil {
			outputError(fmt.Sprintf("Erreur lors de l'ouverture du fichier: %v", err))
			return
		}
		defer f.Close()
		reader = f
	} else {
		reader = os.Stdin
	}

	// Create spillers for large arrays (written to temp files, not memory)
	tickSp, err := newJsonlSpiller("ticks")
	if err != nil {
		outputError(fmt.Sprintf("Failed to create tick spiller: %v", err))
		return
	}
	defer tickSp.Close()

	shotSp, err := newJsonlSpiller("shots")
	if err != nil {
		outputError(fmt.Sprintf("Failed to create shot spiller: %v", err))
		return
	}
	defer shotSp.Close()

	dmgSp, err := newJsonlSpiller("damages")
	if err != nil {
		outputError(fmt.Sprintf("Failed to create damage spiller: %v", err))
		return
	}
	defer dmgSp.Close()

	killSp, err := newJsonlSpiller("kills")
	if err != nil {
		outputError(fmt.Sprintf("Failed to create kill spiller: %v", err))
		return
	}
	defer killSp.Close()

	grenSp, err := newJsonlSpiller("grenades")
	if err != nil {
		outputError(fmt.Sprintf("Failed to create grenade spiller: %v", err))
		return
	}
	defer grenSp.Close()

	sp := &spillers{
		ticks:    tickSp,
		shots:    shotSp,
		damages:  dmgSp,
		kills:    killSp,
		grenades: grenSp,
	}

	tb := &tickBuffer{spiller: tickSp}

	p := dem.NewParser(reader)

	header, err := p.ParseHeader()
	if err != nil {
		outputError(fmt.Sprintf("Erreur lors du parsing du header: %v", err))
		return
	}

	result := ParseResult{
		Success:    true,
		MapName:    header.MapName,
		ServerName: header.ServerName,
		TickRate:   int(p.TickRate()),
		Stats:      []PlayerStats{},
	}

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

	registerFrameHandler(p, &result, bs, srs, tb)
	registerEventHandlers(p, &result, bs, &roundNumber, srs, sp, tb)

	if err := p.ParseToEnd(); err != nil {
		outputError(fmt.Sprintf("Erreur lors du parsing: %v", err))
		return
	}

	// Flush last pending tick
	tb.Flush()

	// Extract final KDA stats
	for _, player := range p.GameState().Participants().All() {
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

	// Free demoinfocs library state (~7 GB) BEFORE JSON output
	p.Close()
	runtime.GC()

	// Stream JSON to stdout: manually assemble the top-level object
	// so that large arrays are streamed from temp files, not from memory.
	w := bufio.NewWriterSize(os.Stdout, 256*1024)
	if err := streamResult(w, &result, sp); err != nil {
		fmt.Fprintf(os.Stderr, "Erreur lors de l'encodage JSON: %v\n", err)
		os.Exit(1)
	}
	w.Flush()
}

// streamResult writes the final JSON object to w, streaming large arrays
// from spillers instead of holding them in memory.
func streamResult(w io.Writer, result *ParseResult, sp *spillers) error {
	// Open object + scalar fields
	fmt.Fprintf(w, `{"success":%t`, result.Success)
	if result.Error != "" {
		errJSON, _ := json.Marshal(result.Error)
		fmt.Fprintf(w, `,"error":%s`, errJSON)
	}
	mapJSON, _ := json.Marshal(result.MapName)
	fmt.Fprintf(w, `,"mapName":%s`, mapJSON)
	if result.ServerName != "" {
		snJSON, _ := json.Marshal(result.ServerName)
		fmt.Fprintf(w, `,"serverName":%s`, snJSON)
	}
	fmt.Fprintf(w, `,"tickRate":%d`, result.TickRate)

	// Stats (small array, encode from memory)
	statsJSON, err := json.Marshal(result.Stats)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, `,"stats":%s`, statsJSON)

	// Stream large arrays from spillers
	fmt.Fprint(w, `,"ticks":`)
	if err := sp.ticks.StreamJSON(w); err != nil {
		return err
	}

	fmt.Fprint(w, `,"shots":`)
	if err := sp.shots.StreamJSON(w); err != nil {
		return err
	}

	fmt.Fprint(w, `,"damages":`)
	if err := sp.damages.StreamJSON(w); err != nil {
		return err
	}

	fmt.Fprint(w, `,"grenadeEvents":`)
	if err := sp.grenades.StreamJSON(w); err != nil {
		return err
	}

	fmt.Fprint(w, `,"kills":`)
	if err := sp.kills.StreamJSON(w); err != nil {
		return err
	}

	// Close object
	fmt.Fprint(w, "}")
	return nil
}

func outputError(msg string) {
	json.NewEncoder(os.Stdout).Encode(ParseResult{Success: false, Error: msg})
	os.Exit(1)
}
