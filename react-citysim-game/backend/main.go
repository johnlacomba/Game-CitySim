package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ===== Basic Types (restored) =====
type PlayerID string

type ZoneType string

const (
	Residential ZoneType = "R"
	Commercial  ZoneType = "C"
	Industrial  ZoneType = "I"
)

type Demand struct {
	Residential int `json:"residential"`
	Commercial  int `json:"commercial"`
	Industrial  int `json:"industrial"`
}

type Player struct {
	ID    PlayerID `json:"id"`
	Name  string   `json:"name"`
	Money int      `json:"money"`
}

type Road struct {
	Owner    PlayerID `json:"owner"`
	PlacedAt int64    `json:"placedAt"`
}
type Zone struct {
	Type     ZoneType `json:"type"`
	Owner    PlayerID `json:"owner"`
	PlacedAt int64    `json:"placedAt"`
}
type Structure struct {
	Type     string   `json:"type"`
	Owner    PlayerID `json:"owner"`
	PlacedAt int64    `json:"placedAt"`
}

type Building struct {
	Type         ZoneType `json:"type"`
	Stage        int      `json:"stage"`
	Final        bool     `json:"final"`
	Residents    int      `json:"residents,omitempty"`
	Employees    int      `json:"employees,omitempty"`
	Supplies     int      `json:"supplies,omitempty"`
	CompletedAt  *int64   `json:"completedAt,omitempty"`
	AbandonPhase int      `json:"abandonPhase,omitempty"`
	IdleTicks    int      `json:"-"`
	Size         int      `json:"size,omitempty"`
	IsRoot       bool     `json:"isRoot,omitempty"`
}

type Tile struct {
	X         int        `json:"x"`
	Y         int        `json:"y"`
	Elevation int        `json:"elevation"`
	Terrain   string     `json:"terrain"`
	Foliage   string     `json:"foliage,omitempty"`
	Zone      *Zone      `json:"zone,omitempty"`
	Road      *Road      `json:"road,omitempty"`
	Structure *Structure `json:"structure,omitempty"`
	Building  *Building  `json:"building,omitempty"`
	Citizens  int        `json:"citizens,omitempty"`
}

type GameState struct {
	Width                int                  `json:"width"`
	Height               int                  `json:"height"`
	Tiles                [][]*Tile            `json:"tiles"`
	Demand               Demand               `json:"demand"`
	Players              map[PlayerID]*Player `json:"players"`
	Tick                 int64                `json:"tick"`
	Population           int                  `json:"population"`
	Employed             int                  `json:"employed"`
	BotID                PlayerID             `json:"botId,omitempty"`
	AILastAction         int64                `json:"-"`
	CitizenGroups        []*CitizenGroup      `json:"citizenGroups,omitempty"`
	PendingResidents     []int                `json:"-"`
	UnemploymentPressure int                  `json:"-"`
	JustRoadThisTick     map[[2]int]int64     `json:"-"`
	Vehicles             []*Vehicle           `json:"vehicles,omitempty"`
	GoodsIC              []*GoodShipment      `json:"goodsIC,omitempty"`
	GoodsCC              []*GoodShipment      `json:"goodsCC,omitempty"`
}

type Vehicle struct {
	ID        int64
	X, Y      float64
	Path      [][2]int
	PathIndex int
}

// global game state mutex & instance
var game *GameState
var gameMu sync.Mutex
var vehicleSeq int64
var goodsSeq int64
var citizenSeq int64

// (Removed old hub implementation duplicate)
// extendRoadIfNeeded now supports straight growth, curves, and perpendicular branching (crossroads/T intersections).
func extendRoadIfNeeded(p *Player) {
	if p.Money < 5 {
		return
	}
	// Prevent forming 2x2 solid road blocks (thickening)
	wouldThicken := func(x, y int) bool {
		for dx := -1; dx <= 0; dx++ {
			for dy := -1; dy <= 0; dy++ {
				ax, ay := x+dx, y+dy
				if !inBounds(ax, ay) || !inBounds(ax+1, ay+1) {
					continue
				}
				// corners of prospective square
				coords := [][2]int{{ax, ay}, {ax + 1, ay}, {ax, ay + 1}, {ax + 1, ay + 1}}
				all := true
				for _, c := range coords {
					tx, ty := c[0], c[1]
					if tx == x && ty == y {
						continue
					} // candidate assumed road
					if game.Tiles[ty][tx].Road == nil {
						all = false
						break
					}
				}
				if all {
					return true
				}
			}
		}
		return false
	}
	tryPlace := func(x, y int) bool {
		if !inBounds(x, y) || wouldThicken(x, y) {
			return false
		}
		t := game.Tiles[y][x]
		if t.Terrain == "water" {
			return false
		}
		if t.Road == nil && t.Zone == nil && t.Structure == nil && t.Building == nil {
			return aiPlaceRoad(p, x, y)
		}
		if t.Road == nil { // bulldoze single obstacle
			t.Zone = nil
			t.Building = nil
			t.Structure = nil
			announce(EventBulldozed, struct {
				X int `json:"x"`
				Y int `json:"y"`
			}{x, y})
			return aiPlaceRoad(p, x, y)
		}
		return false
	}
	type endpoint struct{ x, y, dx, dy int }
	type straightSeg struct {
		x, y  int
		horiz bool
	}
	const pCurve = 0.25
	const pBranch = 0.35 // chance to attempt a perpendicular branch instead of endpoint growth
	attempts := aiMaxRoadAttempts
	for attempts > 0 {
		attempts--
		endpoints := []endpoint{}
		segments := []straightSeg{}
		for y := 0; y < game.Height; y++ {
			for x := 0; x < game.Width; x++ {
				t := game.Tiles[y][x]
				if t.Road == nil {
					continue
				}
				rR := inBounds(x+1, y) && game.Tiles[y][x+1].Road != nil
				rL := inBounds(x-1, y) && game.Tiles[y][x-1].Road != nil
				rD := inBounds(x, y+1) && game.Tiles[y+1][x].Road != nil
				rU := inBounds(x, y-1) && game.Tiles[y-1][x].Road != nil
				cnt := 0
				if rR {
					cnt++
				}
				if rL {
					cnt++
				}
				if rD {
					cnt++
				}
				if rU {
					cnt++
				}
				if cnt == 1 { // endpoint
					var dx, dy int
					if rR {
						dx = 1
					}
					if rL {
						dx = -1
					}
					if rD {
						dy = 1
					}
					if rU {
						dy = -1
					}
					endpoints = append(endpoints, endpoint{x, y, -dx, -dy})
				} else if cnt == 2 { // possible straight for branch
					if rR && rL {
						segments = append(segments, straightSeg{x, y, true})
					}
					if rU && rD {
						segments = append(segments, straightSeg{x, y, false})
					}
				}
			}
		}
		if len(endpoints) == 0 && len(segments) == 0 {
			break
		}
		placed := false
		// branch attempt
		if !placed && len(segments) > 0 && rand.Float64() < pBranch {
			s := segments[rand.Intn(len(segments))]
			// choose ONE perpendicular direction only
			dirs := [][2]int{}
			if s.horiz {
				dirs = [][2]int{{0, 1}, {0, -1}}
			} else {
				dirs = [][2]int{{1, 0}, {-1, 0}}
			}
			rand.Shuffle(len(dirs), func(i, j int) { dirs[i], dirs[j] = dirs[j], dirs[i] })
			for _, d := range dirs {
				if tryPlace(s.x+d[0], s.y+d[1]) {
					placed = true
					break
				}
			}
		}
		// endpoint growth
		if !placed && len(endpoints) > 0 {
			ep := endpoints[rand.Intn(len(endpoints))]
			if rand.Float64() < pCurve { // curve -> pick perpendicular, not both
				var choices [][2]int
				if ep.dx != 0 {
					choices = [][2]int{{0, 1}, {0, -1}}
				} else {
					choices = [][2]int{{1, 0}, {-1, 0}}
				}
				rand.Shuffle(len(choices), func(i, j int) { choices[i], choices[j] = choices[j], choices[i] })
				for _, c := range choices {
					if tryPlace(ep.x+c[0], ep.y+c[1]) {
						placed = true
						break
					}
				}
			}
			if !placed { // straight
				tryPlace(ep.x+ep.dx, ep.y+ep.dy)
			}
		}
		if !placed {
			break
		}
	}
}

// ===== Networking & Events (restored) =====

// Event names sent to frontend
const (
	EventFullState       = "full_state"
	EventZonePlaced      = "zone_placed"
	EventRoadPlaced      = "road_placed"
	EventTick            = "tick"
	EventTrafficUpdate   = "traffic"
	EventBuildingUpdate  = "building_update"
	EventBulldozed       = "bulldozed"
	EventStructurePlaced = "structure_placed"
)

// Client -> Server actions
const (
	ActionPlaceZone      = "place_zone"
	ActionBulldoze       = "bulldoze"
	ActionPlaceStructure = "place_structure"
)

type Envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Payload helper types
type PlaceZonePayload struct {
	X    int      `json:"x"`
	Y    int      `json:"y"`
	Zone ZoneType `json:"zone"`
}
type PlaceRoadPayload struct {
	X int `json:"x"`
	Y int `json:"y"`
}
type BulldozePayload struct {
	X int `json:"x"`
	Y int `json:"y"`
}
type PlaceStructurePayload struct {
	X    int    `json:"x"`
	Y    int    `json:"y"`
	Kind string `json:"kind"`
}
type ZonePlacedEvent struct {
	X    int   `json:"x"`
	Y    int   `json:"y"`
	Zone *Zone `json:"zone"`
}

type Client struct {
	id   PlayerID
	conn *websocket.Conn
	send chan []byte
}
type Hub struct {
	clients    map[*Client]bool
	register   chan *Client
	unregister chan *Client
	broadcast  chan []byte
}

func newHub() *Hub {
	return &Hub{clients: map[*Client]bool{}, register: make(chan *Client), unregister: make(chan *Client), broadcast: make(chan []byte, 256)}
}
func (h *Hub) run() {
	for {
		select {
		case c := <-h.register:
			h.clients[c] = true
		case c := <-h.unregister:
			if h.clients[c] {
				delete(h.clients, c)
				close(c.send)
			}
		case msg := <-h.broadcast:
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
					delete(h.clients, c)
					close(c.send)
				}
			}
		}
	}
}

func (c *Client) reader() {
	defer func() { hub.unregister <- c; c.conn.Close() }()
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env Envelope
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		switch env.Type {
		case ActionPlaceZone:
			var p PlaceZonePayload
			if json.Unmarshal(env.Payload, &p) == nil {
				placeZone(c.id, p)
			}
		case ActionBulldoze:
			var p BulldozePayload
			if json.Unmarshal(env.Payload, &p) == nil {
				bulldoze(c.id, p)
			}
		case ActionPlaceStructure:
			var p PlaceStructurePayload
			if json.Unmarshal(env.Payload, &p) == nil {
				placeStructure(c.id, p)
			}
		}
	}
}
func (c *Client) writer() {
	for msg := range c.send {
		c.conn.WriteMessage(websocket.TextMessage, msg)
	}
}

var hub = newHub()
var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func wsHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "Player"
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	id := PlayerID(uuid.New().String())
	c := &Client{id: id, conn: conn, send: make(chan []byte, 128)}
	gameMu.Lock()
	game.Players[id] = &Player{ID: id, Name: name, Money: 100000}
	gameMu.Unlock()
	hub.register <- c
	go c.writer()
	go c.reader()
	sendFullState(c)
}

func sendFullState(c *Client) {
	gameMu.Lock()
	defer gameMu.Unlock()
	payload, _ := json.Marshal(game)
	env := Envelope{Type: EventFullState, Payload: payload}
	b, _ := json.Marshal(env)
	c.send <- b
}

func placeZone(pid PlayerID, p PlaceZonePayload) {
	if !inBounds(p.X, p.Y) {
		return
	}
	gameMu.Lock()
	defer gameMu.Unlock()
	t := game.Tiles[p.Y][p.X]
	if t.Zone != nil || t.Road != nil || t.Structure != nil {
		return
	}
	pl := game.Players[pid]
	if pl.Money < 100 {
		return
	}
	pl.Money -= 100
	// Clear foliage when zoning
	t.Foliage = ""
	t.Zone = &Zone{Type: p.Zone, Owner: pid, PlacedAt: time.Now().Unix()}
	announce(EventZonePlaced, ZonePlacedEvent{X: p.X, Y: p.Y, Zone: t.Zone})
}
func placeStructure(pid PlayerID, p PlaceStructurePayload) {
	if p.Kind != "power_plant" {
		return
	}
	if !inBounds(p.X, p.Y) {
		return
	}
	gameMu.Lock()
	defer gameMu.Unlock()
	t := game.Tiles[p.Y][p.X]
	if t.Structure != nil || t.Zone != nil || t.Road != nil {
		return
	}
	pl := game.Players[pid]
	if pl.Money < 5000 {
		return
	}
	pl.Money -= 5000
	t.Structure = &Structure{Type: p.Kind, Owner: pid, PlacedAt: time.Now().Unix()}
	announce(EventStructurePlaced, struct {
		X         int        `json:"x"`
		Y         int        `json:"y"`
		Structure *Structure `json:"structure"`
	}{p.X, p.Y, t.Structure})
}

func bulldoze(pid PlayerID, p BulldozePayload) {
	if !inBounds(p.X, p.Y) {
		return
	}
	gameMu.Lock()
	defer gameMu.Unlock()
	t := game.Tiles[p.Y][p.X]
	t.Zone = nil
	t.Building = nil
	t.Road = nil
	t.Structure = nil
	announce(EventBulldozed, struct {
		X int `json:"x"`
		Y int `json:"y"`
	}{p.X, p.Y})
}

type TickSummary struct {
	Tick       int64  `json:"tick"`
	Demand     Demand `json:"demand"`
	Population int    `json:"population"`
	Employed   int    `json:"employed"`
}

type BuildingUpdate struct {
	X        int       `json:"x"`
	Y        int       `json:"y"`
	Building *Building `json:"building"`
}

// progressBuildings advances simple construction stages for zones without final buildings.
func progressBuildings() []BuildingUpdate {
	updates := []BuildingUpdate{}
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			t := game.Tiles[y][x]
			if t.Zone != nil && t.Building == nil { // start
				b := &Building{Type: t.Zone.Type, Stage: 1}
				t.Building = b
				updates = append(updates, BuildingUpdate{X: x, Y: y, Building: b})
			} else if t.Building != nil && !t.Building.Final {
				if t.Building.Stage < 3 {
					t.Building.Stage++
				} else {
					t.Building.Final = true
					ct := time.Now().Unix()
					t.Building.CompletedAt = &ct
				}
				updates = append(updates, BuildingUpdate{X: x, Y: y, Building: t.Building})
			}
		}
	}
	return updates
}

func gameLoop() {
	ticker := time.NewTicker(time.Second)
	for range ticker.C {
		stepGame()
	}
}
func stepGame() {
	gameMu.Lock()
	defer gameMu.Unlock()
	// prune expired short-term road protection entries (prevent zoning over very recent roads)
	if game.JustRoadThisTick == nil {
		game.JustRoadThisTick = map[[2]int]int64{}
	} else {
		for k, exp := range game.JustRoadThisTick {
			if exp <= game.Tick { // expired
				delete(game.JustRoadThisTick, k)
			}
		}
	}
	game.Tick++
	adjustDemand(&game.Demand) // baseline drift
	updates := progressBuildings()
	gt := growthTick()
	if len(gt) > 0 {
		updates = append(updates, gt...)
	}
	simulateCitizens()
	alloc := allocateLaborAndSupplies()
	if len(alloc) > 0 {
		updates = append(updates, alloc...)
	}
	// Employment & demand adjustment
	employmentDemandAdjust(&updates)
	economicTick()
	aiTick()
	// Reconcile building updates after AI actions (e.g., bulldoze+road) so we don't send stale building pointers
	if len(updates) > 0 {
		for i := range updates {
			bx, by := updates[i].X, updates[i].Y
			if inBounds(bx, by) {
				updates[i].Building = game.Tiles[by][bx].Building // current final state (nil if bulldozed)
			}
		}
	}
	if len(updates) > 0 {
		announce(EventBuildingUpdate, struct {
			Updates []BuildingUpdate `json:"updates"`
		}{updates})
	}
	announce(EventTick, gameSummary())
}
func gameSummary() TickSummary {
	return TickSummary{Tick: game.Tick, Demand: game.Demand, Population: game.Population, Employed: game.Employed}
}

// employmentDemandAdjust recalculates employment, adjusts demands and triggers out-migration when sustained unemployment.
func employmentDemandAdjust(updates *[]BuildingUpdate) {
	jobCapacity := 0
	actualEmployees := 0
	industrialEmployees := 0
	commercialEmployees := 0
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			b := game.Tiles[y][x].Building
			if b == nil || !b.Final || b.AbandonPhase > 0 {
				continue
			}
			switch b.Type {
			case Industrial:
				jobCapacity += industrialCapacity
				industrialEmployees += b.Employees
			case Commercial:
				jobCapacity += commercialCapacity
				commercialEmployees += b.Employees
			}
			actualEmployees += b.Employees
		}
	}
	game.Employed = actualEmployees
	unemployed := game.Population - actualEmployees
	if game.Population == 0 {
		return
	}
	ratio := float64(unemployed) / float64(game.Population)
	// Compute residential capacity & open slots fresh for demand basis
	resCap := 0
	resUsed := 0
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			if b := game.Tiles[y][x].Building; b != nil && b.Final && b.Type == Residential && b.AbandonPhase == 0 {
				resCap += 10
				resUsed += b.Residents
			}
		}
	}
	openSlots := resCap - resUsed
	// Residential demand core rule: fewer slots => higher demand.
	if resCap == 0 {
		game.Demand.Residential += 8
	} else if openSlots <= 0 { // full
		game.Demand.Residential += 6
	} else if openSlots < 8 {
		game.Demand.Residential += 3
	} else if openSlots > 50 {
		game.Demand.Residential -= 4
	} else if openSlots > 30 {
		game.Demand.Residential -= 2
	}

	// Job-driven adjustments: if many unfilled jobs (capacity >> population), increase residential to attract workers.
	unfilledJobs := jobCapacity - actualEmployees
	if unfilledJobs > 0 {
		// scale bonus with unfilled ratio but cap
		bonus := unfilledJobs / 10
		if bonus > 6 {
			bonus = 6
		}
		if bonus > 0 {
			game.Demand.Residential += bonus
		}
	}

	// Unemployment handling: if moderate unemployment, lightly push non-residential, but don't tank residential harshly.
	if ratio > 0.25 { // noticeable unemployment
		game.Demand.Industrial += 2
		game.Demand.Commercial += 1
		// light out-migration pressure
		if rand.Float64() < ratio*0.1 {
			removed := 0
			target := 2 + rand.Intn(4)
			for y := 0; y < game.Height && removed < target; y++ {
				for x := 0; x < game.Width && removed < target; x++ {
					b := game.Tiles[y][x].Building
					if b != nil && b.Final && b.Type == Residential && b.Residents > 0 {
						b.Residents--
						removed++
					}
				}
			}
		}
	} else if ratio < 0.05 { // very low unemployment, ease back res demand
		game.Demand.Residential -= 1
	}
}
func adjustDemand(d *Demand) {
	list := []*int{&d.Residential, &d.Commercial, &d.Industrial}
	for _, v := range list {
		*v += rand.Intn(5) - 2
		if *v < -50 {
			*v = -50
		} else if *v > 120 {
			*v = 120
		}
	}
}
func simulateCitizens() {
	// Population = sum of residents in residential buildings
	pop := 0
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			if b := game.Tiles[y][x].Building; b != nil && b.Final && b.Type == Residential {
				pop += b.Residents
			}
		}
	}
	game.Population = pop
	// Employment approximated: total assigned employees (recomputed later)
}

// Allocation & abandonment
const (
	industrialCapacity      = 4
	commercialCapacity      = 2
	commercialSupplyNeed    = 1
	commercialCustomerNeed  = 5
	abandonTriggerTicksBase = 5 // base trigger for R & I
	commercialAbandonFactor = 3 // commercial takes 3x longer
	abandonPhaseTicks       = 3 // ticks spent in black phase before removal
	maxCommercialSupplies   = 8
)

// growthTick: introduce new residents trying to occupy available residential slots.
func growthTick() []BuildingUpdate {
	updates := []BuildingUpdate{}
	// spawn a few new applicants each tick
	newApplicants := 3
	for i := 0; i < newApplicants; i++ {
		game.PendingResidents = append(game.PendingResidents, 0)
	}
	assignedIdx := map[int]bool{}
	for idx, wait := range game.PendingResidents {
		_ = wait
		// find a residential building with space
		var target *Building
		var tx, ty int
		for y := 0; y < game.Height && target == nil; y++ {
			for x := 0; x < game.Width; x++ {
				b := game.Tiles[y][x].Building
				if b != nil && b.Final && b.Type == Residential && b.Residents < 10 && b.AbandonPhase == 0 {
					target = b
					tx = x
					ty = y
					break
				}
			}
		}
		if target != nil {
			target.Residents++
			updates = append(updates, BuildingUpdate{X: tx, Y: ty, Building: target})
			assignedIdx[idx] = true
		}
	}
	// rebuild pending list with incremented waits for unassigned
	newPending := make([]int, 0, len(game.PendingResidents))
	for idx, wait := range game.PendingResidents {
		if assignedIdx[idx] {
			continue
		}
		wait++
		if wait > 5 { // leave city
			continue
		}
		newPending = append(newPending, wait)
	}
	game.PendingResidents = newPending
	return updates
}

func allocateLaborAndSupplies() []BuildingUpdate {
	type ref struct {
		b    *Building
		t    *Tile
		x, y int
	}
	var inds []*Building
	var comm []*Building
	var res []*Building
	refs := []ref{}
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			t := game.Tiles[y][x]
			if b := t.Building; b != nil && b.Final {
				refs = append(refs, ref{b, t, x, y})
				switch b.Type {
				case Industrial:
					inds = append(inds, b)
				case Commercial:
					comm = append(comm, b)
				case Residential:
					res = append(res, b)
				}
			}
		}
	}
	// Persistent workforce model:
	// We no longer reset Employees each tick. Instead we adjust toward a target available worker pool
	// while preserving existing assignments as much as possible. This reduces oscillation and
	// prevents commercial buildings from being repeatedly starved every tick.

	// Desired available workers: fill up to min(jobCapacity, population) to avoid artificial structural unemployment.
	jobCapacity := len(inds)*industrialCapacity + len(comm)*commercialCapacity
	targetWorkers := jobCapacity
	if targetWorkers > game.Population {
		targetWorkers = game.Population
	}
	// Count current workers (only active, non-abandoning buildings matter)
	currentWorkers := 0
	for _, b := range inds {
		if b.AbandonPhase == 0 {
			currentWorkers += b.Employees
		}
	}
	for _, b := range comm {
		if b.AbandonPhase == 0 {
			currentWorkers += b.Employees
		}
	}
	// If we have more workers assigned than target, scale down (remove from Commercial first, then Industrial)
	if currentWorkers > targetWorkers {
		diff := currentWorkers - targetWorkers
		for diff > 0 {
			changed := false
			// Trim commercial employees first (reverse order for mild fairness rotation)
			for i := len(comm) - 1; i >= 0 && diff > 0; i-- {
				b := comm[i]
				if b.AbandonPhase > 0 || b.Employees == 0 {
					continue
				}
				b.Employees--
				diff--
				changed = true
			}
			// Then trim industrial
			for i := len(inds) - 1; i >= 0 && diff > 0; i-- {
				b := inds[i]
				if b.AbandonPhase > 0 || b.Employees == 0 {
					continue
				}
				b.Employees--
				diff--
				changed = true
			}
			if !changed { // nothing to trim
				break
			}
		}
	} else if currentWorkers < targetWorkers { // Need to add workers
		diff := targetWorkers - currentWorkers
		// Step 1: ensure at least 1 worker at each industrial (production base)
		for _, b := range inds {
			if diff == 0 {
				break
			}
			if b.AbandonPhase == 0 && b.Employees == 0 && industrialCapacity > 0 {
				b.Employees = 1
				diff--
			}
		}
		// Step 2: ensure at least 1 worker at each commercial (so they can potentially open)
		for _, b := range comm {
			if diff == 0 {
				break
			}
			if b.AbandonPhase == 0 && b.Employees == 0 && commercialCapacity > 0 {
				b.Employees = 1
				diff--
			}
		}
		// Step 3: fill remaining industrial capacity (prioritize production to curb over-weighted commercial expansion)
		for diff > 0 {
			progress := false
			for _, b := range inds {
				if diff == 0 {
					break
				}
				if b.AbandonPhase == 0 && b.Employees < industrialCapacity {
					b.Employees++
					diff--
					progress = true
				}
			}
			// Step 4: then fill commercial capacity round-robin style
			for _, b := range comm {
				if diff == 0 {
					break
				}
				if b.AbandonPhase == 0 && b.Employees < commercialCapacity {
					b.Employees++
					diff--
					progress = true
				}
			}
			if !progress || diff == 0 {
				break
			}
		}
	}
	// industrial production proportional to employees (1 good per fully staffed 4, so employees/4 rounded up minimal 1 if any)
	produced := 0
	for _, b := range inds {
		if b.Employees > 0 {
			// accumulate produced units
			gain := b.Employees / industrialCapacity
			if b.Employees > 0 && gain == 0 {
				gain = 1
			}
			produced += gain
		}
	}
	// distribute to commercial supplies
	if produced > 0 && len(comm) > 0 {
		for produced > 0 {
			progress := false
			for _, b := range comm {
				if b.Supplies < maxCommercialSupplies {
					b.Supplies++
					produced--
					progress = true
					if produced == 0 {
						break
					}
				}
			}
			if !progress {
				break
			}
		}
	}
	// estimate customers: total residents
	customerPool := 0
	for _, r := range res {
		customerPool += r.Residents
	}
	// evaluate abandonment criteria & phases
	updates := []BuildingUpdate{}
	for _, r := range refs {
		b := r.b
		if b.AbandonPhase > 0 { // countdown
			b.AbandonPhase--
			if b.AbandonPhase == 0 { // remove now
				r.t.Building = nil
				r.t.Zone = nil
				updates = append(updates, BuildingUpdate{X: r.x, Y: r.y, Building: nil})
				continue
			} else {
				updates = append(updates, BuildingUpdate{X: r.x, Y: r.y, Building: b})
				continue
			}
		}
		// determine active criteria (new logic with extended commercial threshold)
		var failing bool
		switch b.Type {
		case Residential:
			failing = (b.Residents == 0)
		case Industrial:
			failing = (b.Employees == 0)
		case Commercial:
			open := (b.Employees >= 1 && b.Supplies >= commercialSupplyNeed && customerPool >= commercialCustomerNeed)
			failing = !open
		}
		if failing {
			b.IdleTicks++
		} else {
			b.IdleTicks = 0
		}
		threshold := abandonTriggerTicksBase
		if b.Type == Commercial {
			threshold = abandonTriggerTicksBase * commercialAbandonFactor
		}
		if b.IdleTicks >= threshold {
			b.IdleTicks = 0
			b.AbandonPhase = abandonPhaseTicks
		}
		updates = append(updates, BuildingUpdate{X: r.x, Y: r.y, Building: b})
	}
	return updates
}
func economicTick() {
	income := game.Employed/10 + game.Population/20
	for _, p := range game.Players {
		p.Money += income
	}
}

const vehicleSpeed = 2.0
const citizenSpeed = 1.5
const goodsSpeed = 2.4

func trafficLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	last := time.Now()
	spawnAcc := time.Duration(0)
	citizenSpawnAcc := time.Duration(0)
	goodsSpawnAcc := time.Duration(0)
	for range ticker.C {
		now := time.Now()
		dt := now.Sub(last).Seconds()
		last = now
		gameMu.Lock()
		updateTraffic(dt)
		updateCitizens(dt)
		updateGoods(dt)
		spawnAcc += 100 * time.Millisecond
		if spawnAcc >= time.Second {
			spawnAcc -= time.Second
			spawnVehicles()
		}
		citizenSpawnAcc += 100 * time.Millisecond
		if citizenSpawnAcc >= 2*time.Second { // spawn citizens every 2s
			citizenSpawnAcc -= 2 * time.Second
			spawnCitizenGroups()
		}
		goodsSpawnAcc += 100 * time.Millisecond
		if goodsSpawnAcc >= 1500*time.Millisecond { // spawn goods roughly every 1.5s
			goodsSpawnAcc -= 1500 * time.Millisecond
			spawnGoodsShipments()
		}
		broadcastTraffic()
		gameMu.Unlock()
	}
}
func updateTraffic(dt float64) {
	if len(game.Vehicles) == 0 {
		return
	}
	move := vehicleSpeed * dt
	kept := game.Vehicles[:0]
	for _, v := range game.Vehicles {
		remain := move
		for remain > 0 && v.PathIndex < len(v.Path) {
			tgt := v.Path[v.PathIndex]
			tx, ty := float64(tgt[0]), float64(tgt[1])
			dx, dy := tx-v.X, ty-v.Y
			dist := abs(dx) + abs(dy)
			if dist <= remain {
				v.X, v.Y = tx, ty
				v.PathIndex++
				remain -= dist
			} else {
				if dx != 0 {
					v.X += remain * sign(dx)
				} else if dy != 0 {
					v.Y += remain * sign(dy)
				}
				remain = 0
			}
		}
		if v.PathIndex < len(v.Path) {
			kept = append(kept, v)
		}
	}
	game.Vehicles = kept
}
func spawnVehicles() {
	desired := game.Population / 25
	if desired > 120 {
		desired = 120
	}
	deficit := desired - len(game.Vehicles)
	if deficit <= 0 {
		return
	}
	if deficit > 8 {
		deficit = 8
	}
	roads := make([][2]int, 0)
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			if game.Tiles[y][x].Road != nil {
				roads = append(roads, [2]int{x, y})
			}
		}
	}
	if len(roads) < 2 {
		return
	}
	for i := 0; i < deficit; i++ {
		a := roads[rand.Intn(len(roads))]
		b := roads[rand.Intn(len(roads))]
		if a == b {
			continue
		}
		path := roadPath(a, b, 200)
		if len(path) < 2 {
			continue
		}
		vehicleSeq++
		v := &Vehicle{ID: vehicleSeq, X: float64(path[0][0]), Y: float64(path[0][1]), Path: path[1:]}
		game.Vehicles = append(game.Vehicles, v)
	}
}
func broadcastTraffic() {
	type V struct {
		ID int64   `json:"id"`
		X  float64 `json:"x"`
		Y  float64 `json:"y"`
	}
	out := make([]V, len(game.Vehicles))
	for i, v := range game.Vehicles {
		out[i] = V{ID: v.ID, X: v.X, Y: v.Y}
	}
	goodsIC := make([]V, len(game.GoodsIC))
	for i, g := range game.GoodsIC {
		goodsIC[i] = V{ID: g.ID, X: g.X, Y: g.Y}
	}
	goodsCC := make([]V, len(game.GoodsCC))
	for i, g := range game.GoodsCC {
		goodsCC[i] = V{ID: g.ID, X: g.X, Y: g.Y}
	}
	// Citizens: include groups that are not "working" (i.e., moving outbound or returning)
	citMoving := make([]V, 0, len(game.CitizenGroups))
	for _, g := range game.CitizenGroups {
		if g.State != "working" { // in transit
			citMoving = append(citMoving, V{ID: g.ID, X: g.X, Y: g.Y})
		}
	}
	announce(EventTrafficUpdate, struct {
		TS       int64 `json:"ts"`
		Vehicles []V   `json:"vehicles"`
		GoodsIC  []V   `json:"goodsIC"`
		GoodsCC  []V   `json:"goodsCC"`
		Citizens []V   `json:"citizens"`
	}{time.Now().UnixNano(), out, goodsIC, goodsCC, citMoving})
}
func roadPath(start, goal [2]int, limit int) [][2]int {
	if start == goal {
		return [][2]int{start}
	}
	type node struct{ x, y int }
	q := []node{{start[0], start[1]}}
	prev := map[[2]int][2]int{}
	vis := map[[2]int]bool{start: true}
	dirs := [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for len(q) > 0 && len(prev) < limit {
		cur := q[0]
		q = q[1:]
		if cur.x == goal[0] && cur.y == goal[1] {
			break
		}
		for _, d := range dirs {
			nx, ny := cur.x+d[0], cur.y+d[1]
			if !inBounds(nx, ny) {
				continue
			}
			if game.Tiles[ny][nx].Road == nil {
				continue
			}
			key := [2]int{nx, ny}
			if !vis[key] {
				vis[key] = true
				prev[key] = [2]int{cur.x, cur.y}
				q = append(q, node{nx, ny})
			}
		}
	}
	if _, ok := prev[goal]; !ok {
		return [][2]int{}
	}
	path := make([][2]int, 0)
	cur := goal
	for cur != start {
		path = append(path, cur)
		cur = prev[cur]
	}
	path = append(path, start)
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

func inBounds(x, y int) bool { return x >= 0 && y >= 0 && x < game.Width && y < game.Height }
func announce(t string, data interface{}) {
	payload, _ := json.Marshal(data)
	env := Envelope{Type: t, Payload: payload}
	b, _ := json.Marshal(env)
	hub.broadcast <- b
}
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// ================= Citizens Simulation =================
type CitizenGroup struct {
	ID               int64
	Count            int
	X, Y             float64
	Path             [][2]int
	PathIndex        int
	State            string  // outbound, working, return
	Timer            float64 // work timer seconds
	OriginX, OriginY int
	DestX, DestY     int
}

// ================= Goods Shipments =================
type GoodShipment struct {
	ID        int64
	X, Y      float64
	Path      [][2]int
	PathIndex int
	Kind      string // "IC" or "CC"
}

func updateGoods(dt float64) {
	if len(game.GoodsIC) == 0 && len(game.GoodsCC) == 0 {
		return
	}
	move := goodsSpeed * dt
	advance := func(src []*GoodShipment) []*GoodShipment {
		kept := src[:0]
		for _, s := range src {
			remain := move
			for remain > 0 && s.PathIndex < len(s.Path) {
				tgt := s.Path[s.PathIndex]
				tx, ty := float64(tgt[0]), float64(tgt[1])
				dx, dy := tx-s.X, ty-s.Y
				dist := abs(dx) + abs(dy)
				if dist <= remain {
					s.X, s.Y = tx, ty
					s.PathIndex++
					remain -= dist
				} else {
					if dx != 0 {
						s.X += remain * sign(dx)
					} else if dy != 0 {
						s.Y += remain * sign(dy)
					}
					remain = 0
				}
			}
			if s.PathIndex < len(s.Path) { // still traveling
				kept = append(kept, s)
			}
		}
		return kept
	}
	game.GoodsIC = advance(game.GoodsIC)
	game.GoodsCC = advance(game.GoodsCC)
}

func spawnGoodsShipments() { // cap total
	if len(game.GoodsIC)+len(game.GoodsCC) > 300 {
		return
	}
	inds := make([][2]int, 0)
	comm := make([][2]int, 0)
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			t := game.Tiles[y][x]
			if t.Building != nil && t.Building.Final {
				switch t.Building.Type {
				case Industrial:
					inds = append(inds, [2]int{x, y})
				case Commercial:
					comm = append(comm, [2]int{x, y})
				}
			}
		}
	}
	if len(inds) > 0 && len(comm) > 0 { // spawn IC
		for tries := 0; tries < 3; tries++ {
			a := inds[rand.Intn(len(inds))]
			b := comm[rand.Intn(len(comm))]
			ax, ay, ok1 := adjacentRoad(a[0], a[1])
			bx, by, ok2 := adjacentRoad(b[0], b[1])
			if !ok1 || !ok2 {
				continue
			}
			p := roadPath([2]int{ax, ay}, [2]int{bx, by}, 400)
			if len(p) < 2 {
				continue
			}
			goodsSeq++
			s := &GoodShipment{ID: goodsSeq, X: float64(p[0][0]), Y: float64(p[0][1]), Path: p[1:], Kind: "IC"}
			game.GoodsIC = append(game.GoodsIC, s)
			break
		}
	}
	if len(comm) > 1 { // spawn CC
		for tries := 0; tries < 3; tries++ {
			a := comm[rand.Intn(len(comm))]
			b := comm[rand.Intn(len(comm))]
			if a == b {
				continue
			}
			ax, ay, ok1 := adjacentRoad(a[0], a[1])
			bx, by, ok2 := adjacentRoad(b[0], b[1])
			if !ok1 || !ok2 {
				continue
			}
			p := roadPath([2]int{ax, ay}, [2]int{bx, by}, 400)
			if len(p) < 2 {
				continue
			}
			goodsSeq++
			s := &GoodShipment{ID: goodsSeq, X: float64(p[0][0]), Y: float64(p[0][1]), Path: p[1:], Kind: "CC"}
			game.GoodsCC = append(game.GoodsCC, s)
			break
		}
	}
}

func spawnCitizenGroups() {
	// limit number of active groups
	if len(game.CitizenGroups) > 200 {
		return
	}
	// collect residential and job tiles
	res := make([][2]int, 0)
	jobs := make([][2]int, 0)
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			t := game.Tiles[y][x]
			if t.Building != nil && t.Building.Final {
				if t.Building.Type == Residential {
					res = append(res, [2]int{x, y})
				} else if t.Building.Type == Commercial || t.Building.Type == Industrial {
					jobs = append(jobs, [2]int{x, y})
				}
			}
		}
	}
	if len(res) == 0 || len(jobs) == 0 {
		return
	}
	tries := 0
	for tries < 3 {
		tries++
		r := res[rand.Intn(len(res))]
		j := jobs[rand.Intn(len(jobs))]
		// find adjacent road tiles
		orx, ory, ok1 := adjacentRoad(r[0], r[1])
		drx, dry, ok2 := adjacentRoad(j[0], j[1])
		if !ok1 || !ok2 {
			continue
		}
		roadPathSeg := roadPath([2]int{orx, ory}, [2]int{drx, dry}, 400)
		if len(roadPathSeg) == 0 {
			continue
		}
		// build full path: origin -> road entry -> ... -> road exit -> destination
		path := make([][2]int, 0, len(roadPathSeg)+2)
		path = append(path, [2]int{r[0], r[1]})
		path = append(path, roadPathSeg...)
		path = append(path, [2]int{j[0], j[1]})
		if len(path) < 2 {
			continue
		}
		citizenSeq++
		count := 3 + rand.Intn(6) // 3-8
		g := &CitizenGroup{ID: citizenSeq, Count: count, X: float64(path[0][0]), Y: float64(path[0][1]), Path: path[1:], State: "outbound", OriginX: r[0], OriginY: r[1], DestX: j[0], DestY: j[1]}
		// remove citizens from origin immediately
		game.Tiles[r[1]][r[0]].Citizens -= count
		if game.Tiles[r[1]][r[0]].Citizens < 0 {
			game.Tiles[r[1]][r[0]].Citizens = 0
		}
		game.CitizenGroups = append(game.CitizenGroups, g)
		break
	}
}

func adjacentRoad(x, y int) (int, int, bool) {
	dirs := [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for _, d := range dirs {
		nx, ny := x+d[0], y+d[1]
		if !inBounds(nx, ny) {
			continue
		}
		if game.Tiles[ny][nx].Road != nil {
			return nx, ny, true
		}
	}
	return 0, 0, false
}

func updateCitizens(dt float64) {
	if len(game.CitizenGroups) == 0 {
		return
	}
	speed := citizenSpeed * dt
	kept := game.CitizenGroups[:0]
	for _, g := range game.CitizenGroups {
		if g.State == "working" {
			g.Timer -= dt
			if g.Timer <= 0 { // start return trip
				// build return path (reverse) origin path: current position is at destination tile
				// path back: destination adjacent road -> ... -> origin adjacent road -> origin tile
				drx, dry, ok2 := adjacentRoad(g.DestX, g.DestY)
				orx, ory, ok1 := adjacentRoad(g.OriginX, g.OriginY)
				if ok1 && ok2 {
					roadSeg := roadPath([2]int{drx, dry}, [2]int{orx, ory}, 400)
					revPath := make([][2]int, 0, len(roadSeg)+2)
					revPath = append(revPath, roadSeg...)
					revPath = append(revPath, [2]int{g.OriginX, g.OriginY})
					g.Path = revPath
					g.PathIndex = 0
					g.State = "return"
					// remove from destination tile
					destTile := game.Tiles[g.DestY][g.DestX]
					destTile.Citizens -= g.Count
					if destTile.Citizens < 0 {
						destTile.Citizens = 0
					}
				} else { // can't find path back -> drop group
					continue
				}
			} else {
				kept = append(kept, g)
				continue
			}
		}
		if g.PathIndex < len(g.Path) {
			remain := speed
			for remain > 0 && g.PathIndex < len(g.Path) {
				tgt := g.Path[g.PathIndex]
				tx, ty := float64(tgt[0]), float64(tgt[1])
				dx, dy := tx-g.X, ty-g.Y
				dist := abs(dx) + abs(dy)
				if dist <= remain {
					g.X, g.Y = tx, ty
					g.PathIndex++
					remain -= dist
				} else {
					if dx != 0 {
						g.X += remain * sign(dx)
					} else if dy != 0 {
						g.Y += remain * sign(dy)
					}
					remain = 0
				}
			}
		}
		// arrival handling
		if g.PathIndex >= len(g.Path) {
			if g.State == "outbound" { // arrived at destination
				g.State = "working"
				g.Timer = 5 + rand.Float64()*10 // 5-15 seconds
				destTile := game.Tiles[g.DestY][g.DestX]
				// If destination is commercial with zero supplies and zero employees, citizens give up and leave city (do not add to tile)
				if destTile.Building != nil && destTile.Building.Type == Commercial && destTile.Building.Supplies == 0 && destTile.Building.Employees == 0 {
					// citizens leave: do not enter working state, they vanish (simulate leaving city)
					continue
				}
				destTile.Citizens += g.Count
				kept = append(kept, g)
			} else if g.State == "return" { // final arrival origin
				originTile := game.Tiles[g.OriginY][g.OriginX]
				originTile.Citizens += g.Count
				// group finished; not kept
			} else {
				kept = append(kept, g)
			}
		} else {
			kept = append(kept, g)
		}
	}
	game.CitizenGroups = kept
}

// ================= AI BOT =================
const (
	aiActionInterval    = 4    // act more frequently
	aiZoneAttempts      = 2    // zoning still conservative
	aiRoadExtendChance  = 0.9  // most cycles attempt a road push
	aiZoneAfterRoadBias = 0.35 // slightly lower zoning after road (focus on corridor first)
	aiMaxRoadAttempts   = 3    // try multiple successive extensions per action
)

func createBotLocked() {
	if game.BotID != "" {
		return
	}
	id := PlayerID(uuid.New().String())
	game.Players[id] = &Player{ID: id, Name: "Planner", Money: 50000}
	game.BotID = id
	log.Println("AI bot created", id)
}

func aiTick() {
	if game.BotID == "" {
		return
	}
	if game.Tick-game.AILastAction < aiActionInterval {
		return
	}
	p := game.Players[game.BotID]
	if p == nil || p.Money < 200 {
		return
	}
	ensureSomeRoads(p)
	// Decide whether to extend road first; higher frequency keeps corridors open
	roadDone := false
	if rand.Float64() < aiRoadExtendChance {
		extendRoadIfNeeded(p)
		roadDone = true
	}
	// Only zone if we did not build a road OR we allow a zone after road based on bias.
	if !roadDone || rand.Float64() < aiZoneAfterRoadBias {
		z := pickZoneTypeByDemand()
		placed := 0
		for i := 0; i < aiZoneAttempts; i++ {
			x, y, ok := findZoneSpotNearRoad()
			if !ok {
				break
			}
			if game.JustRoadThisTick != nil {
				if exp, ok := game.JustRoadThisTick[[2]int{x, y}]; ok && exp > game.Tick {
					continue
				}
			}
			// Skip spot if zoning here would fully encase a single-road corridor (leave at least one orthogonal empty neighbor)
			if encasesRoad(x, y) {
				continue
			}
			if aiPlaceZone(p, x, y, z) {
				placed++
			}
		}
	}
	// AI tick done
}

// pickZoneTypeByDemand chooses the highest current demand; ties favor Residential -> Commercial -> Industrial
func pickZoneTypeByDemand() ZoneType {
	d := game.Demand
	unemployed := game.Population - game.Employed
	if unemployed < 0 {
		unemployed = 0
	}

	// Compute open residential slots for awareness (mirrors demand logic)
	resCap := 0
	resUsed := 0
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			if b := game.Tiles[y][x].Building; b != nil && b.Final && b.Type == Residential && b.AbandonPhase == 0 {
				resCap += 10
				resUsed += b.Residents
			}
		}
	}
	openRes := resCap - resUsed

	// Base scores from raw demand values
	rScore := d.Residential
	cScore := d.Commercial + 5 // +5% commercial bias
	iScore := d.Industrial

	// Penalize industrial if already high relative to unemployment (avoid overbuilding I when no workers idle)
	if unemployed < 5 {
		iScore -= 8 // strong penalty when virtually no idle workers
	} else if unemployed < 15 {
		iScore -= 4
	}
	// Encourage residential if housing is tight
	if openRes <= 0 { // totally full
		rScore += 10
	} else if openRes < 10 {
		rScore += 5
	}
	// Mild encouragement for commercial if some unemployed exist but housing not critically tight
	if unemployed > 10 && openRes > 5 {
		cScore += 2
	}

	// Pick highest score; ties favor Residential then Commercial then Industrial
	best := Residential
	bestVal := rScore
	if cScore > bestVal {
		best = Commercial
		bestVal = cScore
	}
	if iScore > bestVal {
		best = Industrial
	}
	return best
}

func findZoneSpotNearRoad() (int, int, bool) {
	roads := make([][2]int, 0)
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			if game.Tiles[y][x].Road != nil {
				roads = append(roads, [2]int{x, y})
			}
		}
	}
	if len(roads) == 0 {
		return 0, 0, false
	}
	// partial shuffle
	for i := 0; i < len(roads) && i < 32; i++ {
		j := rand.Intn(len(roads))
		roads[i], roads[j] = roads[j], roads[i]
	}
	dirs := [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for _, r := range roads {
		for _, d := range dirs {
			nx, ny := r[0]+d[0], r[1]+d[1]
			if !inBounds(nx, ny) {
				continue
			}
			t := game.Tiles[ny][nx]
			if t.Zone == nil && t.Road == nil && t.Structure == nil && t.Terrain != "water" {
				return nx, ny, true
			}
		}
	}
	return 0, 0, false
}

func aiPlaceZone(p *Player, x, y int, z ZoneType) bool {
	t := game.Tiles[y][x]
	if t.Zone != nil || t.Road != nil || t.Structure != nil || t.Terrain == "water" {
		return false
	}
	if p.Money < 100 {
		return false
	}
	p.Money -= 100
	t.Zone = &Zone{Type: z, Owner: p.ID, PlacedAt: time.Now().Unix()}
	announce(EventZonePlaced, ZonePlacedEvent{X: x, Y: y, Zone: t.Zone})
	return true
}

func ensureSomeRoads(p *Player) {
	count := 0
	for y := 0; y < game.Height; y++ {
		for x := 0; x < game.Width; x++ {
			if game.Tiles[y][x].Road != nil {
				count++
			}
		}
	}
	if count > 0 {
		return
	}
	cx, cy := game.Width/2, game.Height/2
	for dx := -3; dx <= 3; dx++ {
		aiPlaceRoad(p, cx+dx, cy)
	}
	for dy := -3; dy <= 3; dy++ {
		aiPlaceRoad(p, cx, cy+dy)
	}
}

// (Removed legacy BFS-based extendRoadIfNeeded; linear version defined earlier)

func aiPlaceRoad(p *Player, x, y int) bool {
	if !inBounds(x, y) {
		return false
	}
	t := game.Tiles[y][x]
	if t.Road != nil || t.Zone != nil || t.Structure != nil || t.Terrain == "water" {
		return false
	}
	if p.Money < 20 {
		return false
	}
	p.Money -= 20
	t.Road = &Road{Owner: p.ID, PlacedAt: time.Now().Unix()}
	if game.JustRoadThisTick != nil {
		game.JustRoadThisTick[[2]int{x, y}] = game.Tick + 2
	}
	announce(EventRoadPlaced, struct {
		X    int   `json:"x"`
		Y    int   `json:"y"`
		Road *Road `json:"road"`
	}{x, y, t.Road})
	return true
}

// encasesRoad returns true if placing a zone at (x,y) would box in a road tile so that no further straight extension is possible.
// Simple heuristic: if exactly one adjacent road exists AND all other empty orthogonal tiles are either out of bounds or already zoned/road/structure/water.
func encasesRoad(x, y int) bool {
	if !inBounds(x, y) {
		return false
	}
	t := game.Tiles[y][x]
	if t.Zone != nil || t.Road != nil || t.Structure != nil || t.Terrain == "water" {
		return false
	}
	dirs := [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	roadCount := 0
	openAlternatives := 0
	for _, d := range dirs {
		nx, ny := x+d[0], y+d[1]
		if !inBounds(nx, ny) {
			continue
		}
		nt := game.Tiles[ny][nx]
		if nt.Road != nil {
			roadCount++
		} else if nt.Zone == nil && nt.Structure == nil && nt.Terrain != "water" {
			openAlternatives++
		}
	}
	if roadCount == 1 && openAlternatives == 0 {
		return true
	}
	return false
}

// newGame initializes a default game state
func newGame() *GameState {
	w, h := 64, 64
	g := &GameState{Width: w, Height: h, Demand: Demand{Residential: 10, Commercial: 5, Industrial: 5}, Players: map[PlayerID]*Player{}, Tiles: make([][]*Tile, h)}
	for y := 0; y < h; y++ {
		row := make([]*Tile, w)
		for x := 0; x < w; x++ {
			row[x] = &Tile{X: x, Y: y, Elevation: 0, Terrain: "grass"}
		}
		g.Tiles[y] = row
	}
	return g
}

func main() {
	game = newGame()
	go hub.run()
	go gameLoop()
	go trafficLoop()
	gameMu.Lock()
	createBotLocked()
	gameMu.Unlock()
	http.HandleFunc("/ws", wsHandler)
	log.Println("Server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
