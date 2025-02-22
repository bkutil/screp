/*

Package repparser implements StarCraft: Brood War replay parsing.

The package is safe for concurrent use.

Information sources:

BWHF replay parser:

https://github.com/icza/bwhf/tree/master/src/hu/belicza/andras/bwhf/control

BWAPI replay parser:

https://github.com/bwapi/bwapi/tree/master/bwapi/libReplayTool

https://github.com/bwapi/bwapi/tree/master/bwapi/include/BWAPI

https://github.com/bwapi/bwapi/tree/master/bwapi/PKLib

Command models:

https://github.com/icza/bwhf/blob/master/src/hu/belicza/andras/bwhf/model/Action.java

https://github.com/bwapi/bwapi/tree/master/bwapi/libReplayTool


jssuh replay parser:

https://github.com/neivv/jssuh

Map Data format:

http://www.staredit.net/wiki/index.php/Scenario.chk

http://blog.naver.com/PostView.nhn?blogId=wisdomswrap&logNo=60119755717&parentCategoryNo=&categoryNo=19&viewDate=&isShowPopularPosts=false&from=postView

*/
package repparser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"runtime"
	"sort"
	"time"

	"github.com/icza/screp/rep"
	"github.com/icza/screp/rep/repcmd"
	"github.com/icza/screp/rep/repcore"
	"github.com/icza/screp/repparser/repdecoder"
)

const (
	// Version is a Semver2 compatible version of the parser.
	Version = "v1.2.1"
)

var (
	// ErrNotReplayFile indicates the given file (or reader) is not a valid
	// replay file
	ErrNotReplayFile = errors.New("not a replay file")

	// ErrParsing indicates that an unexpected error occurred, which may be
	// due to corrupt / invalid replay file, or some implementation error.
	ErrParsing = errors.New("parsing")
)

// ParseFile parses all sections from an SC:BW replay file.
func ParseFile(name string) (r *rep.Replay, err error) {
	return ParseFileSections(name, true, true)
}

// ParseFileSections parses an SC:BW replay file.
// Parsing commands and map data sections depends on the given parameters.
// Replay ID and header sections are always parsed.
func ParseFileSections(name string, commands, mapData bool) (r *rep.Replay, err error) {
	dec, err := repdecoder.NewFromFile(name)
	if err != nil {
		return nil, err
	}
	defer dec.Close()

	return parseProtected(dec, commands, mapData)
}

// Parse parses all sections of an SC:BW replay from the given byte slice.
func Parse(repData []byte) (*rep.Replay, error) {
	return ParseSections(repData, true, true)
}

// ParseSections parses an SC:BW replay from the given byte slice.
// Parsing commands and map data sections depends on the given parameters.
// Replay ID and header sections are always parsed.
func ParseSections(repData []byte, commands, mapData bool) (*rep.Replay, error) {
	dec := repdecoder.New(repData)
	defer dec.Close()

	return parseProtected(dec, commands, mapData)
}

// parseProtected calls parse(), but protects the function call from panics,
// in which case it returns ErrParsing.
func parseProtected(dec repdecoder.Decoder, commands, mapData bool) (r *rep.Replay, err error) {
	// Input is untrusted data, protect the parsing logic.
	// It also protects against implementation bugs.
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Parsing error: %v", r)
			buf := make([]byte, 2000)
			n := runtime.Stack(buf, false)
			log.Printf("Stack: %s", buf[:n])
			err = ErrParsing
		}
	}()

	return parse(dec, commands, mapData)
}

// Section describes a Section of the replay.
type Section struct {
	// ID of the section
	ID int

	// Size of the uncompressed section in bytes;
	// 0 means the Size has to be read as a section of 4 bytes
	Size int32

	// ParseFunc defines the function responsible to process (parse / interpret)
	// the section's data.
	ParseFunc func(data []byte, r *rep.Replay) error
}

// Sections describes the subsequent Sections of replays
var Sections = []*Section{
	{0, 0x04, parseReplayID},
	{1, 0x279, parseHeader},
	{2, 0, parseCommands},
	{3, 0, parseMapData},
}

// Named sections
var (
	SectionReplayID = Sections[0]
	SectionHeader   = Sections[1]
	SectionCommands = Sections[2]
	SectionMapData  = Sections[3]
)

// parse parses an SC:BW replay using the given Decoder.
func parse(dec repdecoder.Decoder, commands, mapData bool) (*rep.Replay, error) {
	r := new(rep.Replay)

	// Determine last section that needs to be decoded / parsed:
	var lastSection *Section
	switch {
	case mapData:
		lastSection = SectionMapData
	case commands:
		lastSection = SectionCommands
	default:
		lastSection = SectionHeader
	}

	// A replay is a sequence of sections:
	for _, s := range Sections {
		if err := dec.NewSection(); err != nil {
			return nil, fmt.Errorf("Decoder.NewSection() error: %v", err)
		}

		// Determine section size:
		size := s.Size
		if size == 0 {
			sizeData, err := dec.Section(4)
			if err != nil {
				return nil, fmt.Errorf("Decoder.Section() error when reading size: %v", err)
			}
			size = int32(binary.LittleEndian.Uint32(sizeData))
		}

		// Read section data
		data, err := dec.Section(size)
		if err != nil && s.ID == SectionReplayID.ID {
			err = ErrNotReplayFile // In case of Replay ID section return special error
		}
		if err != nil {
			return nil, fmt.Errorf("Decoder.Section() error: %v", err)
		}

		// Need to process?
		switch {
		case s == SectionCommands && !commands:
		case s == SectionMapData && !mapData:
		default:
			// Process section data
			if err = s.ParseFunc(data, r); err != nil {
				return nil, fmt.Errorf("ParseFunc() error (sectionID: %d): %v", s.ID, err)
			}
		}

		if s == lastSection {
			break
		}
	}

	return r, nil
}

// repIDs is the possible valid content of the Replay ID section
var repIDs = [][]byte{
	[]byte("seRS"), // Starting from 1.21
	[]byte("reRS"), // Up until 1.20. Abbreviation for replay ReSource?
}

// parseReplayID processes the replay ID data.
func parseReplayID(data []byte, r *rep.Replay) (err error) {
	for _, repID := range repIDs {
		if bytes.Equal(data, repID) {
			return
		}
	}

	return ErrNotReplayFile
}

// parseHeader processes the replay header data.
func parseHeader(data []byte, r *rep.Replay) error {
	bo := binary.LittleEndian // ByteOrder reader: little-endian

	h := new(rep.Header)
	r.Header = h

	h.Engine = repcore.EngineByID(data[0x00])
	h.Frames = repcore.Frame(bo.Uint32(data[0x01:]))
	h.StartTime = time.Unix(int64(bo.Uint32(data[0x08:])), 0) // replay stores seconds since EPOCH
	h.Title = cString(data[0x18 : 0x18+28])
	h.MapWidth = bo.Uint16(data[0x34:])
	h.MapHeight = bo.Uint16(data[0x36:])
	h.AvailSlotsCount = data[0x39]
	h.Speed = repcore.SpeedByID(data[0x3a])
	h.Type = repcore.GameTypeByID(bo.Uint16(data[0x3c:]))
	h.SubType = bo.Uint16(data[0x3e:])
	h.Host = cString(data[0x48 : 0x48+24])
	h.Map = cString(data[0x61 : 0x61+26])

	// Parse players
	const (
		slotsCount = 12
		maxPlayers = 8
	)
	h.PIDPlayers = make(map[byte]*rep.Player, slotsCount)
	h.Slots = make([]*rep.Player, slotsCount)
	playerStructs := data[0xa1 : 0xa1+432]
	for i := range h.Slots {
		p := new(rep.Player)
		h.Slots[i] = p
		ps := playerStructs[i*36 : i*36+432/slotsCount]
		p.SlotID = bo.Uint16(ps)
		p.ID = ps[4]
		p.Type = repcore.PlayerTypeByID(ps[8])
		p.Race = repcore.RaceByID(ps[9])
		p.Team = ps[10]
		p.Name = cString(ps[11 : 11+25])

		if i < maxPlayers {
			p.Color = repcore.ColorByID(bo.Uint32(data[0x251+i*4:]))
		}

		h.PIDPlayers[p.ID] = p

		// Filter real players:
		if p.Name != "" {
			h.OrigPlayers = append(h.OrigPlayers, p)
		}
	}

	// Fill Players in team order:
	h.Players = make([]*rep.Player, len(h.OrigPlayers))
	copy(h.Players, h.OrigPlayers)
	sort.SliceStable(h.Players, func(i int, j int) bool {
		return h.Players[i].Team < h.Players[j].Team
	})

	return nil
}

// parseCommands processes the players' commands data.
func parseCommands(data []byte, r *rep.Replay) error {
	bo := binary.LittleEndian // ByteOrder reader: little-endian

	_ = bo
	cs := new(rep.Commands)
	r.Commands = cs

	for sr, size := (sliceReader{b: data}), uint32(len(data)); sr.pos < size; {
		frame := sr.getUint32()

		// Command block in this frame
		cmdBlockSize := sr.getByte()                    // cmd block size (remaining)
		cmdBlockEndPos := sr.pos + uint32(cmdBlockSize) // Cmd block end position

		for sr.pos < cmdBlockEndPos {
			parseOk := true

			var cmd repcmd.Cmd
			base := &repcmd.Base{
				Frame: repcore.Frame(frame),
			}
			base.PlayerID = sr.getByte()
			base.Type = repcmd.TypeByID(sr.getByte())

			switch base.Type.ID { // Try to list in frequency order:

			case repcmd.TypeIDRightClick:
				rccmd := &repcmd.RightClickCmd{Base: base}
				rccmd.Pos.X = sr.getUint16()
				rccmd.Pos.Y = sr.getUint16()
				rccmd.UnitTag = repcmd.UnitTag(sr.getUint16())
				rccmd.Unit = repcmd.UnitByID(sr.getUint16())
				rccmd.Queued = sr.getByte() != 0
				cmd = rccmd

			case repcmd.TypeIDSelect, repcmd.TypeIDSelectAdd, repcmd.TypeIDSelectRemove:
				count := sr.getByte()
				selectCmd := &repcmd.SelectCmd{
					Base:     base,
					UnitTags: make([]repcmd.UnitTag, count),
				}
				for i := byte(0); i < count; i++ {
					selectCmd.UnitTags[i] = repcmd.UnitTag(sr.getUint16())
				}
				cmd = selectCmd

			case repcmd.TypeIDHotkey:
				hotkeyCmd := &repcmd.HotkeyCmd{Base: base}
				hotkeyCmd.HotkeyType = repcmd.HotkeyTypeByID(sr.getByte())
				hotkeyCmd.Group = sr.getByte()
				cmd = hotkeyCmd

			case repcmd.TypeIDTrain, repcmd.TypeIDUnitMorph:
				cmd = &repcmd.TrainCmd{
					Base: base,
					Unit: repcmd.UnitByID(sr.getUint16()),
				}

			case repcmd.TypeIDTargetedOrder:
				tocmd := &repcmd.TargetedOrderCmd{Base: base}
				tocmd.Pos.X = sr.getUint16()
				tocmd.Pos.Y = sr.getUint16()
				tocmd.UnitTag = repcmd.UnitTag(sr.getUint16())
				tocmd.Unit = repcmd.UnitByID(sr.getUint16())
				tocmd.Order = repcmd.OrderByID(sr.getByte())
				tocmd.Queued = sr.getByte() != 0
				cmd = tocmd

			case repcmd.TypeIDBuild:
				buildCmd := &repcmd.BuildCmd{Base: base}
				buildCmd.Order = repcmd.OrderByID(sr.getByte())
				buildCmd.Pos.X = sr.getUint16()
				buildCmd.Pos.Y = sr.getUint16()
				buildCmd.Unit = repcmd.UnitByID(sr.getUint16())
				cmd = buildCmd

			case repcmd.TypeIDStop, repcmd.TypeIDBurrow, repcmd.TypeIDUnburrow,
				repcmd.TypeIDReturnCargo, repcmd.TypeIDHoldPosition, repcmd.TypeIDUnloadAll,
				repcmd.TypeIDUnsiege, repcmd.TypeIDSiege, repcmd.TypeIDCloack, repcmd.TypeIDDecloack:
				cmd = &repcmd.QueueableCmd{
					Base:   base,
					Queued: sr.getByte() != 0,
				}

			case repcmd.TypeIDLeaveGame:
				cmd = &repcmd.LeaveGameCmd{
					Base:   base,
					Reason: repcmd.LeaveReasonByID(sr.getByte()),
				}

			case repcmd.TypeIDMinimapPing:
				pingCmd := &repcmd.MinimapPingCmd{Base: base}
				pingCmd.Pos.X = sr.getUint16()
				pingCmd.Pos.Y = sr.getUint16()
				cmd = pingCmd

			case repcmd.TypeIDChat:
				chatCmd := &repcmd.ChatCmd{Base: base}
				chatCmd.TargetPlayerID = sr.getByte()
				chatCmd.Message = cString(sr.readSlice(80))
				cmd = chatCmd

			case repcmd.TypeIDVision:
				cmd = &repcmd.GeneralCmd{
					Base: base,
					Data: sr.readSlice(2),
				}

			case repcmd.TypeIDAlliance:
				cmd = &repcmd.GeneralCmd{
					Base: base,
					Data: sr.readSlice(4),
				}

			case repcmd.TypeIDGameSpeed:
				cmd = &repcmd.GameSpeedCmd{
					Base:  base,
					Speed: repcore.SpeedByID(sr.getByte()),
				}

			case repcmd.TypeIDCancelTrain:
				cmd = &repcmd.CancelTrainCmd{
					Base:    base,
					UnitTag: repcmd.UnitTag(sr.getUint16()),
				}

			case repcmd.TypeIDUnload:
				cmd = &repcmd.GeneralCmd{
					Base: base,
					Data: sr.readSlice(2),
				}

			case repcmd.TypeIDLiftOff:
				liftOffCmd := &repcmd.LiftOffCmd{Base: base}
				liftOffCmd.Pos.X = sr.getUint16()
				liftOffCmd.Pos.Y = sr.getUint16()
				cmd = liftOffCmd

			case repcmd.TypeIDTech:
				cmd = &repcmd.TechCmd{
					Base: base,
					Tech: repcmd.TechByID(sr.getByte()),
				}

			case repcmd.TypeIDUpgrade:
				cmd = &repcmd.UpgradeCmd{
					Base:    base,
					Upgrade: repcmd.UpgradeByID(sr.getByte()),
				}

			case repcmd.TypeIDBuildingMorph:
				cmd = &repcmd.BuildingMorphCmd{
					Base: base,
					Unit: repcmd.UnitByID(sr.getUint16()),
				}

			case repcmd.TypeIDLatency:
				cmd = &repcmd.LatencyCmd{
					Base:    base,
					Latency: repcmd.LatencyTypeByID(sr.getByte()),
				}

			case repcmd.TypeIDCheat:
				cmd = &repcmd.GeneralCmd{
					Base: base,
					Data: sr.readSlice(4),
				}

			case repcmd.TypeIDSaveGame, repcmd.TypeIDLoadGame:
				count := sr.getUint32()
				sr.pos += count

			// NO ADDITIONAL DATA:

			case repcmd.TypeIDKeepAlive:
			case repcmd.TypeIDRestartGame:
			case repcmd.TypeIDPause:
			case repcmd.TypeIDResume:
			case repcmd.TypeIDCancelBuild:
			case repcmd.TypeIDCancelMorph:
			case repcmd.TypeIDCarrierStop:
			case repcmd.TypeIDReaverStop:
			case repcmd.TypeIDOrderNothing:
			case repcmd.TypeIDTrainFighter:
			case repcmd.TypeIDMergeArchon:
			case repcmd.TypeIDCancelNuke:
			case repcmd.TypeIDCancelTech:
			case repcmd.TypeIDCancelUpgrade:
			case repcmd.TypeIDCancelAddon:
			case repcmd.TypeIDStim:
			case repcmd.TypeIDVoiceEnable:
			case repcmd.TypeIDVoiceDisable:
			case repcmd.TypeIDStartGame:
			case repcmd.TypeIDBriefingStart:
			case repcmd.TypeIDMergeDarkArchon:
			case repcmd.TypeIDMakeGamePublic:

			// DON'T CARE COMMANDS:

			case repcmd.TypeIDSync:
				sr.pos += 6
			case repcmd.TypeIDVoiceSquelch:
				sr.pos++
			case repcmd.TypeIDVoiceUnsquelch:
				sr.pos++
			case repcmd.TypeIDDownloadPercentage:
				sr.pos++
			case repcmd.TypeIDChangeGameSlot:
				sr.pos += 5
			case repcmd.TypeIDNewNetPlayer:
				sr.pos += 7
			case repcmd.TypeIDJoinedGame:
				sr.pos += 17
			case repcmd.TypeIDChangeRace:
				sr.pos += 2
			case repcmd.TypeIDTeamGameTeam:
				sr.pos++
			case repcmd.TypeIDUMSTeam:
				sr.pos++
			case repcmd.TypeIDMeleeTeam:
				sr.pos += 2
			case repcmd.TypeIDSwapPlayers:
				sr.pos += 2
			case repcmd.TypeIDSavedData:
				sr.pos += 12
			case repcmd.TypeIDReplaySpeed:
				sr.pos += 9

			// New commands introduced in 1.21

			case 0x60: // Some kind of Move ?
				cmd = &repcmd.GeneralCmd{
					Base: base,
					Data: sr.readSlice(11),
				}
			case 0x61:
				cmd = &repcmd.GeneralCmd{
					Base: base,
					Data: sr.readSlice(12),
				}
			case 0x62:
				cmd = &repcmd.GeneralCmd{
					Base: base,
					Data: sr.readSlice(4),
				}
			case repcmd.TypeIDSelect121:
				count := sr.getByte()
				selectCmd := &repcmd.SelectCmd{
					Base:     base,
					UnitTags: make([]repcmd.UnitTag, count),
				}
				for i := byte(0); i < count; i++ {
					selectCmd.UnitTags[i] = repcmd.UnitTag(sr.getUint16())
					sr.getUint16() // Unknown, always 0?
				}
				cmd = selectCmd
			case 0x64, 0x65:
				count := sr.getByte()
				cmd = &repcmd.GeneralCmd{
					Base: base,
					Data: sr.readSlice(uint32(count) * 4),
				}

			default:
				// We don't know how to parse this command, we have to skip
				// to the end of the command block
				// (potentially skipping additional commands...)
				fmt.Printf("skipping typeID: %#v, frame: %d, playerID: %d, remaining bytes: %d [% x]\n", base.Type.ID, base.Frame, base.PlayerID, cmdBlockEndPos-sr.pos, sr.b[sr.pos:cmdBlockEndPos])
				pec := &repcmd.ParseErrCmd{Base: base}
				if len(cs.Cmds) > 0 {
					pec.PrevCmd = cs.Cmds[len(cs.Cmds)-1]
				}
				cs.ParseErrCmds = append(cs.ParseErrCmds, pec)
				sr.pos = cmdBlockEndPos
				parseOk = false
			}

			if parseOk {
				if cmd == nil {
					cs.Cmds = append(cs.Cmds, base)
				} else {
					cs.Cmds = append(cs.Cmds, cmd)
				}
			}
		}

		sr.pos = cmdBlockEndPos
	}

	return nil
}

// parseMapData processes the map data data.
func parseMapData(data []byte, r *rep.Replay) error {
	md := new(rep.MapData)
	r.MapData = md

	// Map data section is a sequence of sub-sections:
	for sr, size := (sliceReader{b: data}), uint32(len(data)); sr.pos < size; {
		id := sr.getString(4)
		ssSize := sr.getUint32()    // sub-section size (remaining)
		ssEndPos := sr.pos + ssSize // sub-section end position

		switch id {
		case "VER ":
			md.Version = sr.getUint16()
		case "ERA ": // Tile set sub-section
			md.TileSet = repcore.TileSetByID(sr.getUint16() & 0x07)
		case "DIM ": // Dimension sub-section
			// If map has a non-standard size, the replay header contains
			// invalid map size, this is the correct one.
			width := sr.getUint16()
			height := sr.getUint16()
			if width <= 256 && height <= 256 {
				if width > r.Header.MapWidth {
					r.Header.MapWidth = sr.getUint16()
				}
				if height > r.Header.MapHeight {
					r.Header.MapHeight = sr.getUint16()
				}
			}
		case "MTXM": // Tile sub-section
			// map_width*map_height (a tile is an uint16 value)
			maxI := ssSize / 2
			// Note: Sometimes map is broken into multiple sections.
			// The first one is the biggest (whole map size),
			// but the beginning of map is empty. The subsequent MTXM
			// sub-sections will fill the whole at the beginning.
			if len(md.Tiles) == 0 {
				md.Tiles = make([]uint16, maxI)
			}
			for i := uint32(0); i < maxI; i++ {
				md.Tiles[i] = sr.getUint16()
			}
		case "UNIT": // Unit sub-section
			// TODO When all UnitIDs (enums) are introduced, use those
			const (
				unitIDMinField1  = 0xb0
				unitIDMinField2  = 0xb1
				unitIDMinField3  = 0xb2
				unitIDVespGeyser = 0xbc
				unitIDStartLoc   = 0xd6
			)
			for sr.pos < ssEndPos {
				unitEndPos := sr.pos + 36 // 36 bytes for each unit

				sr.pos += 4 // uint32 unit class instance ("serial number")
				x := sr.getUint16()
				y := sr.getUint16()
				unitID := sr.getUint16()
				sr.pos += 2             // uint16 Type of relation to another building (i.e. add-on, nydus link)
				sr.pos += 2             // uint16 Flags of special properties (e.g. cloacked, burrowed etc.)
				sr.pos += 2             // uint16 valid elements flag
				ownerID := sr.getByte() // 0-based SlotID

				switch unitID {
				case unitIDMinField1, unitIDMinField2, unitIDMinField3:
					md.MineralFields = append(md.MineralFields, repcore.Point{X: x, Y: y})
				case unitIDVespGeyser:
					md.Geysers = append(md.Geysers, repcore.Point{X: x, Y: y})
				case unitIDStartLoc:
					md.StartLocations = append(md.StartLocations,
						rep.StartLocation{Point: repcore.Point{X: x, Y: y}, SlotID: ownerID},
					)
				}

				// Skip unprocessed unit data:
				sr.pos = unitEndPos
			}
		}

		// Part or all of the sub-section might be unprocessed, skip the unprocessed bytes
		sr.pos = ssEndPos
	}

	return nil
}

// cString returns a 0x00 byte terminated string from the given buffer.
func cString(data []byte) string {
	// Find 0x00 byte:
	for i, ch := range data {
		if ch == 0 {
			return string(data[:i]) // excludes terminating 0x00
		}
	}

	// Couldn't find? As a fallback, just return the whole as-is:
	return string(data)
}
