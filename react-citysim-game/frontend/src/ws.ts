export type ZoneType = 'R'|'C'|'I';

export interface Zone { type: ZoneType; owner: string; placedAt: number; }
export interface Building { stage:number; final:boolean; type:ZoneType; completedAt?:number; residents?:number; employees?:number; supplies?:number; abandonPhase?:number }
export interface Tile { x:number; y:number; elevation:number; terrain:string; foliage?:string; zone?:Zone; building?:Building; road?: { owner:string; placedAt:number } }
export interface Demand { residential:number; commercial:number; industrial:number }
export interface Player { id:string; name:string; money:number }
export interface FullState { width:number; height:number; tiles:Tile[][]; demand:Demand; players:Record<string, Player>; tick:number; conn?: GameConnection }
export interface TickSummary { tick:number; demand:Demand; population:number; employed:number }
export interface TrafficPayload { ts:number; vehicles:{id:number;x:number;y:number}[]; goodsIC?:{id:number;x:number;y:number}[]; goodsCC?:{id:number;x:number;y:number}[]; citizens?:{id:number;x:number;y:number}[] }
export interface BuildingUpdatePayload { updates:{x:number;y:number; building:Building|null}[] }

export interface ZonePlacedPayload { x:number; y:number; zone: Zone }
export interface RoadPlacedPayload { x:number; y:number; road:{ owner:string; placedAt:number } }

export interface Envelope<T=any> { type:string; payload:T }

const EventFullState = 'full_state';
const EventZonePlaced = 'zone_placed';
const EventRoadPlaced = 'road_placed';
const EventTick = 'tick';
const EventTraffic = 'traffic';
const EventBuildingUpdate = 'building_update';
const EventBulldozed = 'bulldozed';
const ActionPlaceZone = 'place_zone';
export interface GameConnection {
  ws: WebSocket;
  placeZone: (x:number,y:number,zone:ZoneType)=>void;
  onFullState?: (gs:FullState)=>void;
  onTick?: (t:TickSummary)=>void;
  onZonePlaced?: (z:ZonePlacedPayload)=>void;
  onRoadPlaced?: (r:RoadPlacedPayload)=>void;
  onTraffic?: (t:TrafficPayload)=>void;
  onBuildingUpdate?: (b:BuildingUpdatePayload)=>void;
  onBulldozed?: (b:{x:number;y:number})=>void;
  close: ()=>void;
}

export function connect(opts:{name:string}): GameConnection {
  const ws = new WebSocket(`ws://localhost:8080/ws?name=${encodeURIComponent(opts.name)}`);
  const conn: GameConnection = {
    ws,
    placeZone(x,y,zone){
      const payload = {x,y,zone};
      const env:Envelope = {type: ActionPlaceZone, payload};
      ws.send(JSON.stringify(env));
    },
    close(){ ws.close(); }
  };
  ws.onmessage = ev => {
    const env:Envelope = JSON.parse(ev.data);
    switch(env.type){
      case EventFullState:
        const gs = env.payload as FullState; gs.conn = conn; conn.onFullState?.(gs); break;
      case EventTick:
        conn.onTick?.(env.payload as TickSummary); break;
      case EventZonePlaced:
        conn.onZonePlaced?.(env.payload as ZonePlacedPayload); break;
      case EventRoadPlaced:
        conn.onRoadPlaced?.(env.payload as RoadPlacedPayload); break;
      case EventTraffic:
        conn.onTraffic?.(env.payload as TrafficPayload); break;
      case EventBuildingUpdate:
        conn.onBuildingUpdate?.(env.payload as BuildingUpdatePayload); break;
      case EventBulldozed:
        conn.onBulldozed?.(env.payload as any); break;
    }
  };
  return conn;
}
