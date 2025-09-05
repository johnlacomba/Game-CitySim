import React, { useEffect, useRef, useState } from 'react';
// Explicit extension helps some tooling; TypeScript allows either
import { connect, FullState, ZonePlacedPayload, ZoneType, TickSummary, RoadPlacedPayload, TrafficPayload } from '../ws';

const TILE_W = 64; // base diamond width
const TILE_H = 32; // base diamond height

interface Camera { x:number; y:number; zoom:number; }

export const Game: React.FC = () => {
  const canvasRef = useRef<HTMLCanvasElement|null>(null);
  const [money, setMoney] = useState(0);
  const [demand, setDemand] = useState({residential:0, commercial:0, industrial:0});
  const [tick,setTick] = useState(0);
  const [zoneTool,setZoneTool] = useState<ZoneType|'none'|'road'|'bulldoze'>('R');
  const [pop,setPop] = useState({pop:0, emp:0});
  const stateRef = useRef<FullState|null>(null);
  const cam = useRef<Camera>({x:0,y:0,zoom:1});
  const hoverRef = useRef<{x:number;y:number}|null>(null);
  const [hoverDetails,setHoverDetails] = useState<{screenX:number;screenY:number; lines:string[]} | null>(null);
  // Raw snapshots from server
  const vehiclesRef = useRef<{id:number;x:number;y:number}[]>([]);
  const goodsICRef = useRef<{id:number;x:number;y:number}[]>([]); // yellow
  const goodsCCRef = useRef<{id:number;x:number;y:number}[]>([]); // blue
  const citizensRef = useRef<{id:number;x:number;y:number}[]>([]); // moving citizens (black)
  const citizensBlueRef = useRef<{id:number;x:number;y:number}[]>([]); // blue return shoppers
  const citizensYellowRef = useRef<{id:number;x:number;y:number}[]>([]); // yellow deliveries outbound
  // Per-vehicle animation state for continuous smoothing
  interface VehicleAnim { id:number; x:number; y:number; tx:number; ty:number; speed:number; }
  const vehicleStatesRef = useRef<Map<number,VehicleAnim>>(new Map());
  const goodsICStatesRef = useRef<Map<number,VehicleAnim>>(new Map());
  const goodsCCStatesRef = useRef<Map<number,VehicleAnim>>(new Map());
  const citizenStatesRef = useRef<Map<number,VehicleAnim>>(new Map());
  const citizenBlueStatesRef = useRef<Map<number,VehicleAnim>>(new Map());
  const citizenYellowStatesRef = useRef<Map<number,VehicleAnim>>(new Map());
  const trafficTSRef = useRef<number>(0);
  const lastFrameRef = useRef<number>(0);
  const animRAF = useRef<number|undefined>(undefined);

  useEffect(()=>{
    const c = connect({name:`Player${Math.floor(Math.random()*999)}`});
    c.onFullState = (gs:FullState) => { stateRef.current = gs; setTick(gs.tick); setDemand(gs.demand); draw(); };
  c.onTick = (t:TickSummary) => { setTick(t.tick); setDemand(t.demand); setPop({pop:t.population, emp:t.employed});
    // Reconciliation: clear impossible combos road+zone/building (server never keeps these)
    const gs = stateRef.current; if(gs){
      for(let y=0;y<gs.height;y++){
        for(let x=0;x<gs.width;x++){
          const tile:any = gs.tiles[y][x];
          if(tile.road && (tile.zone || tile.building)){
            // Prefer building/zone; road must have been stale
            tile.road = undefined;
          }
        }
      }
    }
  };
  c.onZonePlaced = (zp:ZonePlacedPayload) => { if(stateRef.current){ const tile = stateRef.current.tiles[zp.y][zp.x];
      // Server guarantees no road+zone, but clear any stale road that client may have kept
      (tile as any).road = undefined;
      tile.zone = zp.zone; draw(); } };
  c.onRoadPlaced = (rp:RoadPlacedPayload) => { if(stateRef.current){ const tile = stateRef.current.tiles[rp.y][rp.x];
      // Defensive: clear possible stale zone/building if missed bulldoze event
      tile.zone = undefined; tile.building = undefined;
      (tile as any).road = rp.road; draw(); } };
  c.onBuildingUpdate = (bp:any) => { if(stateRef.current){ for(const u of bp.updates){ const t = stateRef.current.tiles[u.y][u.x];
        t.building = u.building || undefined;
        if(u.building){ // ensure no stale road if a building now occupies tile
          (t as any).road = undefined;
        } else { // building removed; if also no zone, state remains cleared
          t.zone = undefined;
        }
      } draw(); } };
  c.onBulldozed = (b:{x:number;y:number}) => { if(stateRef.current){ const t = stateRef.current.tiles[b.y][b.x]; t.zone=undefined; t.building=undefined; (t as any).road=undefined; (t as any).structure=undefined; draw(); } };
  c.onTraffic = (tp:TrafficPayload) => {
    vehiclesRef.current = tp.vehicles;
    goodsICRef.current = tp.goodsIC||[];
    goodsCCRef.current = tp.goodsCC||[];
    citizensRef.current = tp.citizens||[];
    citizensBlueRef.current = tp.citizensRG||[];
    citizensYellowRef.current = tp.citizensY||[];
    trafficTSRef.current = tp.ts;
    const base = 1.05;
    function updateSet(arr:{id:number;x:number;y:number}[], map:Map<number,VehicleAnim>){
      const seen = new Set<number>();
      for(const v of arr){
        seen.add(v.id);
        const existing = map.get(v.id);
        if(!existing){ map.set(v.id,{id:v.id,x:v.x,y:v.y,tx:v.x,ty:v.y,speed:0}); }
        else { const dx = v.x - existing.x; const dy = v.y - existing.y; const dist = Math.hypot(dx,dy); existing.tx = v.x; existing.ty = v.y; existing.speed = dist * base; }
      }
      for(const id of Array.from(map.keys())) if(!seen.has(id)) map.delete(id);
    }
    updateSet(tp.vehicles, vehicleStatesRef.current);
    updateSet(goodsICRef.current, goodsICStatesRef.current);
    updateSet(goodsCCRef.current, goodsCCStatesRef.current);
    updateSet(citizensRef.current, citizenStatesRef.current);
    updateSet(citizensBlueRef.current, citizenBlueStatesRef.current);
    updateSet(citizensYellowRef.current, citizenYellowStatesRef.current);
  };
    return ()=> c.close();
  },[]);

  useEffect(()=>{ draw(); },[tick]);

  useEffect(()=>{
    function loop(ts:number){
      const last = lastFrameRef.current || ts;
      const dt = Math.min(0.1, (ts - last)/1000); // clamp to avoid large jumps
      lastFrameRef.current = ts;
      // Advance animation states (vehicles + goods)
      function advance(map:Map<number,VehicleAnim>){
        if(!map.size) return;
        for(const st of map.values()){
          const dx = st.tx - st.x; const dy = st.ty - st.y; const dist = Math.hypot(dx,dy);
          if(dist > 0.0001){
            if(dist > 8){ st.x = st.tx; st.y = st.ty; continue; }
            const move = st.speed * dt;
            if(move >= dist){ st.x = st.tx; st.y = st.ty; }
            else { const r = move/dist; st.x += dx*r; st.y += dy*r; }
          }
        }
      }
      advance(vehicleStatesRef.current);
      advance(goodsICStatesRef.current);
  advance(goodsCCStatesRef.current);
  advance(citizenStatesRef.current);
      advance(citizenBlueStatesRef.current);
      advance(citizenYellowStatesRef.current);
      draw();
      animRAF.current = requestAnimationFrame(loop);
    }
    animRAF.current = requestAnimationFrame(loop);
    return ()=> { if(animRAF.current) cancelAnimationFrame(animRAF.current); };
  },[]);

  // Keyboard camera movement
  useEffect(()=>{
    const speed = 40;
    function onKey(e:KeyboardEvent){
      let moved = false;
      switch(e.key){
        case 'ArrowUp': case 'w': case 'W': cam.current.y += speed; moved=true; break;
        case 'ArrowDown': case 's': case 'S': cam.current.y -= speed; moved=true; break;
        case 'ArrowLeft': case 'a': case 'A': cam.current.x -= speed; moved=true; break;
        case 'ArrowRight': case 'd': case 'D': cam.current.x += speed; moved=true; break;
      }
      if(moved){ e.preventDefault(); draw(); }
    }
    window.addEventListener('keydown', onKey);
    return ()=> window.removeEventListener('keydown', onKey);
  },[]);

  function screenToMap(px:number, py:number){
    const canvas = canvasRef.current!;
    const z = cam.current.zoom;
    const originX = canvas.width/2 - cam.current.x;
    const originY = 50 + cam.current.y; // must match draw()
    const w2 = (TILE_W/2)*z;
    const h2 = (TILE_H/2)*z;
    const rx = px - originX;
    const ry = py - originY;
    const mapX = (rx / w2 + ry / h2) / 2;
    const mapY = (ry / h2 - rx / w2) / 2;
    return {x: Math.floor(mapX), y: Math.floor(mapY)};
  }

  function handleClick(e: React.MouseEvent){
    if(zoneTool==='none') return;
    const rect = (e.target as HTMLCanvasElement).getBoundingClientRect();
    const map = screenToMap(e.clientX-rect.left, e.clientY-rect.top);
    if(!stateRef.current) return;
    if(map.x<0||map.y<0||map.x>=stateRef.current.width||map.y>=stateRef.current.height) return;
    const tile = stateRef.current.tiles[map.y][map.x];
    if(zoneTool==='road'){
      if((tile as any).road || tile.terrain==='water' || tile.zone) return;
      stateRef.current.conn?.ws.send(JSON.stringify({type:'place_road', payload:{x:map.x,y:map.y}}));
      return;
    }
    if(zoneTool==='bulldoze'){
      stateRef.current.conn?.ws.send(JSON.stringify({type:'bulldoze', payload:{x:map.x,y:map.y}}));
      return;
    }
    if(tile.zone || tile.terrain==='water') return;
    stateRef.current.conn?.placeZone(map.x,map.y,zoneTool as ZoneType);
  }

  function draw(){
    const gs = stateRef.current; if(!gs) return;
    const canvas = canvasRef.current!;
    const ctx = canvas.getContext('2d')!;
    canvas.width = window.innerWidth; canvas.height= window.innerHeight;
    ctx.clearRect(0,0,canvas.width, canvas.height);

    const z = cam.current.zoom;
    const originX = canvas.width/2 - cam.current.x;
    const originY = 50 + cam.current.y;

    // Determine visible rough bounds for basic culling
    const maxDim = Math.max(gs.width, gs.height);
    for(let y=0;y<gs.height;y++){
      for(let x=0;x<gs.width;x++){
        const sx = (x - y) * TILE_W/2 * z + originX;
  const sy = (x + y) * TILE_H/2 * z + originY;
        if(sx < -100 || sy < -100 || sx > canvas.width+100 || sy > canvas.height+100) continue;
        const t = gs.tiles[y][x];
  drawTile(ctx, sx, sy, z, t);
      }
    }

    // Hover highlight
    if(hoverRef.current){
      const {x,y} = hoverRef.current;
      if(x>=0 && y>=0 && x<gs.width && y<gs.height){
        const sx = (x - y) * TILE_W/2 * z + originX;
  const sy = (x + y) * TILE_H/2 * z + originY;
        ctx.save();
        ctx.translate(sx,sy);
        const w = TILE_W*z; const h = TILE_H*z;
        ctx.beginPath(); ctx.moveTo(0,h/2); ctx.lineTo(w/2,0); ctx.lineTo(w,h/2); ctx.lineTo(w/2,h); ctx.closePath();
        ctx.strokeStyle = 'rgba(255,255,255,0.6)'; ctx.lineWidth = 2; ctx.stroke();
        ctx.restore();
      }
    }

  // Vehicles suppressed (green dots removed per request)
    // Goods IC (yellow) smoothed
    if(goodsICStatesRef.current.size){
      ctx.save(); ctx.fillStyle = '#ffeb3b';
      for(const st of goodsICStatesRef.current.values()){
        const sx = (st.x - st.y) * TILE_W/2 * z + originX;
        const sy = (st.x + st.y) * TILE_H/2 * z + originY;
        if(sx < -60 || sy < -60 || sx > canvas.width+60 || sy > canvas.height+60) continue;
        ctx.beginPath(); ctx.arc(sx, sy + (TILE_H/4)*z, 3*z, 0, Math.PI*2); ctx.fill();
      }
      ctx.restore();
    }
    // Goods CC (blue) smoothed
    if(goodsCCStatesRef.current.size){
      ctx.save(); ctx.fillStyle = '#3399ff';
      for(const st of goodsCCStatesRef.current.values()){
        const sx = (st.x - st.y) * TILE_W/2 * z + originX;
        const sy = (st.x + st.y) * TILE_H/2 * z + originY;
        if(sx < -60 || sy < -60 || sx > canvas.width+60 || sy > canvas.height+60) continue;
        ctx.beginPath(); ctx.arc(sx, sy + (TILE_H/4)*z, 3*z, 0, Math.PI*2); ctx.fill();
      }
      ctx.restore();
    }
    // Citizens black
    if(citizenStatesRef.current.size){
      ctx.save(); ctx.fillStyle = '#000';
      for(const st of citizenStatesRef.current.values()){ const sx = (st.x - st.y) * TILE_W/2 * z + originX; const sy = (st.x + st.y) * TILE_H/2 * z + originY; if(sx<-60||sy<-60||sx>canvas.width+60||sy>canvas.height+60) continue; ctx.beginPath(); ctx.arc(sx, sy + (TILE_H/4)*z, 2.5*z, 0, Math.PI*2); ctx.fill(); }
      ctx.restore();
    }
    // Citizens blue
    if(citizenBlueStatesRef.current.size){
      ctx.save(); ctx.fillStyle = '#3399ff';
      for(const st of citizenBlueStatesRef.current.values()){ const sx = (st.x - st.y) * TILE_W/2 * z + originX; const sy = (st.x + st.y) * TILE_H/2 * z + originY; if(sx<-60||sy<-60||sx>canvas.width+60||sy>canvas.height+60) continue; ctx.beginPath(); ctx.arc(sx, sy + (TILE_H/4)*z, 2.5*z, 0, Math.PI*2); ctx.fill(); }
      ctx.restore();
    }
    // Citizens yellow
    if(citizenYellowStatesRef.current.size){
      ctx.save(); ctx.fillStyle = '#ffeb3b';
      for(const st of citizenYellowStatesRef.current.values()){ const sx = (st.x - st.y) * TILE_W/2 * z + originX; const sy = (st.x + st.y) * TILE_H/2 * z + originY; if(sx<-60||sy<-60||sx>canvas.width+60||sy>canvas.height+60) continue; ctx.beginPath(); ctx.arc(sx, sy + (TILE_H/4)*z, 2.5*z, 0, Math.PI*2); ctx.fill(); }
      ctx.restore();
    }
  }

  useEffect(()=>{
    function onResize(){ draw(); }
    window.addEventListener('resize', onResize);
    return ()=> window.removeEventListener('resize', onResize);
  },[]);

  return <>
    <canvas ref={canvasRef} 
      onClick={handleClick}
      onMouseMove={(e)=>{
        const rect = (e.target as HTMLCanvasElement).getBoundingClientRect();
        const map = screenToMap(e.clientX-rect.left, e.clientY-rect.top); hoverRef.current = map; draw();
        const gs = stateRef.current;
        if(gs && map.x>=0 && map.y>=0 && map.x<gs.width && map.y<gs.height){
          const t:any = gs.tiles[map.y][map.x];
          const lines:string[] = [];
          lines.push(`Tile (${map.x},${map.y}) terrain: ${t.terrain}`);
          if(t.foliage && !t.zone && !t.building && !t.road){ lines.push(`Foliage: ${t.foliage}`); }
          if(t.zone){ lines.push(`Zone: ${t.zone.type}`); } else { lines.push('Zone: none'); }
          if(t.road){ lines.push('Road: yes'+ (t.intersection? ' (intersection)':'')); }
          if(t.building){
            const b = t.building;
            lines.push(`Building: ${b.type} stage ${b.stage}${b.final? ' (final)':''}`);
            if(b.abandonPhase && b.abandonPhase>0){ lines.push(`Status: abandoning (${b.abandonPhase})`); }
            // capacities
            if(b.type==='R'){
              lines.push(`Residents: ${b.residents||0}/10`);
            } else if(b.type==='C') {
              lines.push(`Employees: ${b.employees||0}/2`);
              lines.push(`Supplies: ${b.supplies||0}`);
            } else if(b.type==='I') {
              lines.push(`Employees: ${b.employees||0}/4`);
              lines.push(`Output (goods stock abstract): produced via employees`);
            }
          } else if(t.zone){
            lines.push('Building: (none yet)');
          }
          setHoverDetails({screenX:e.clientX, screenY:e.clientY, lines});
        } else {
          setHoverDetails(null);
        }
      }}
      onWheel={(e)=>{ if(e.deltaY!==0){ const z = cam.current.zoom * (e.deltaY<0?1.1:0.9); cam.current.zoom = Math.min(2, Math.max(0.5,z)); draw(); }}}
      style={{width:'100%',height:'100%', cursor: zoneTool==='none'? 'default':'pointer'}} />
    <div className="hud">
  Tick: {tick} | Demand R:{demand.residential} C:{demand.commercial} I:{demand.industrial} | Pop {pop.pop} Emp {pop.emp} Unemp {Math.max(0, pop.pop - pop.emp)}
      {/* TODO show player money when server sends per-player delta or we know our id */}
    </div>
    <div className="toolbar">
  {['none','R','C','I','road','bulldoze'].map(z=><button key={z} className={zoneTool===z? 'active':''} onClick={()=>setZoneTool(z as any)}>{z}</button>)}
    </div>
    {hoverDetails && (
      <div style={{position:'fixed', left:hoverDetails.screenX+12, top:hoverDetails.screenY+12, background:'rgba(30,30,30,0.85)', color:'#eee', padding:'6px 8px', border:'1px solid #222', borderRadius:4, fontSize:12, pointerEvents:'none', maxWidth:240, lineHeight:1.3, zIndex:10}}>
        {hoverDetails.lines.map((l,i)=><div key={i}>{l}</div>)}
      </div>
    )}
  </>
}

function drawTile(ctx:CanvasRenderingContext2D, x:number, y:number, z:number, tile:any){
  const w = TILE_W*z; const h = TILE_H*z;
  ctx.save(); ctx.translate(x,y);
  ctx.beginPath(); ctx.moveTo(0, h/2); ctx.lineTo(w/2,0); ctx.lineTo(w, h/2); ctx.lineTo(w/2,h); ctx.closePath();
  let fill = '#3a5';
  if(tile.terrain==='water') fill='#246'; else if(tile.terrain==='hill') fill='#555'; else if(tile.terrain==='forest') fill='#274';
  ctx.fillStyle = fill; ctx.fill();
  ctx.strokeStyle = '#000'; ctx.lineWidth = 1; ctx.stroke();
  if(tile.road && !tile.zone && !tile.building){
    if(tile.intersection){
      ctx.fillStyle = '#888'; // lighter or distinct shade for intersection
    } else {
      ctx.fillStyle = '#666';
    }
    ctx.fill();
  }
  if(tile.zone){
    const zc = tile.zone.type;
    // Darker green for Residential, keep existing for others
    ctx.fillStyle = zc==='R'? 'rgba(0,140,0,0.35)': zc==='C'? 'rgba(0,0,255,0.3)':'rgba(255,255,0,0.3)';
    ctx.fill();
  }
  // Foliage (only if no road / zone / building so it doesn't overlap placed content)
  if(!tile.building && !tile.zone && !tile.road && tile.foliage){
    ctx.save();
    const centerX = w/2; const centerY = h/2; // diamond center
    const scale = z; // base scale factor
    switch(tile.foliage){
      case 'tree': {
        // trunk
        ctx.fillStyle = '#5b3a12';
        ctx.fillRect(centerX-2*scale, centerY-6*scale, 4*scale, 6*scale);
        // canopy
        ctx.beginPath();
        ctx.fillStyle = '#1f5d20';
        ctx.arc(centerX, centerY-8*scale, 6*scale, 0, Math.PI*2);
        ctx.fill();
        ctx.beginPath();
        ctx.arc(centerX-4*scale, centerY-6*scale, 5*scale, 0, Math.PI*2); ctx.fill();
        ctx.beginPath();
        ctx.arc(centerX+4*scale, centerY-6*scale, 5*scale, 0, Math.PI*2); ctx.fill();
        break;
      }
      case 'bush': {
        ctx.fillStyle = '#2f6d3a';
        ctx.beginPath(); ctx.arc(centerX-3*scale, centerY-2*scale, 4*scale, 0, Math.PI*2); ctx.fill();
        ctx.beginPath(); ctx.arc(centerX+2*scale, centerY-2*scale, 5*scale, 0, Math.PI*2); ctx.fill();
        ctx.beginPath(); ctx.arc(centerX, centerY-4*scale, 4*scale, 0, Math.PI*2); ctx.fill();
        break;
      }
      case 'rock': {
        ctx.fillStyle = '#b7b9bd';
        ctx.beginPath();
        ctx.moveTo(centerX-5*scale, centerY-2*scale);
        ctx.lineTo(centerX-2*scale, centerY-6*scale);
        ctx.lineTo(centerX+4*scale, centerY-5*scale);
        ctx.lineTo(centerX+6*scale, centerY-1*scale);
        ctx.lineTo(centerX+2*scale, centerY+2*scale);
        ctx.lineTo(centerX-3*scale, centerY+1*scale);
        ctx.closePath(); ctx.fill();
        break;
      }
      case 'tall_grass': {
        ctx.strokeStyle = '#3f8d3f'; ctx.lineWidth = 1.2*scale;
        for(let i=0;i<6;i++){
          const off = (i-3)*2*scale;
            ctx.beginPath();
            ctx.moveTo(centerX+off, centerY+2*scale);
            ctx.lineTo(centerX+off + (i%2===0? -2:2)*scale, centerY-6*scale);
            ctx.stroke();
        }
        break;
      }
      case 'dirt': {
        ctx.fillStyle = '#7b5a33';
        ctx.globalAlpha = 0.85;
        ctx.beginPath();
        ctx.ellipse(centerX, centerY, 10*scale, 6*scale, 0, 0, Math.PI*2);
        ctx.fill();
        ctx.globalAlpha = 1;
        break;
      }
      default: break;
    }
    ctx.restore();
  }
  if(tile.building){
    const b = tile.building;
    if(!b.isRoot && b.size>1){ ctx.restore(); return; }
    const stage = b.stage; const final = b.final; const size = b.size || 1;
    // Buildings under construction (not final) use brown; finished use type color
    let color: string;
    if(b.abandonPhase && b.abandonPhase>0){
      color = '#000';
    } else if(!final){
      color = '#8b5a2b'; // brown construction
    } else if(b.type==='R') color = '#2e8b2e';
    else if(b.type==='C') color = '#66aaff';
    else if(b.type==='I') color = '#d4d452';
    else color = '#ccc';
    ctx.fillStyle = color;
    // Multi-size footprint diamond stack: draw one larger prism scaled by size
    const scale = size; // number of tiles per side
    const footprintW = w * scale;
    const footprintH = h * scale;
    // offset so root tile anchors bottom center roughly
    ctx.save();
    ctx.translate(- (scale-1)*w/2, - (scale-1)*h/2);
    // simple extruded rectangle for now (can refine to iso box)
  const bw = (w*0.5) * scale; const bh = (h*0.6 + stage*4) * (0.6 + 0.2*scale);
  const bx = (footprintW/2) - bw/2; const by = (footprintH/2) - bh;
  ctx.fillRect(bx, by, bw, bh);
  // thin black border
  ctx.strokeStyle = '#000';
  ctx.lineWidth = Math.max(1, 1*z);
  ctx.strokeRect(bx + 0.5, by + 0.5, bw - 1, bh - 1);
    ctx.restore();
  }
  ctx.restore();
}

function shade(col:string, amt:number){
  // expects #rrggbb
  const num = parseInt(col.slice(1),16);
  let r = (num>>16)&255, g=(num>>8)&255, b=num&255;
  r = Math.min(255, Math.max(0, r+amt));
  g = Math.min(255, Math.max(0, g+amt));
  b = Math.min(255, Math.max(0, b+amt));
  return '#'+(r.toString(16).padStart(2,'0'))+(g.toString(16).padStart(2,'0'))+(b.toString(16).padStart(2,'0'));
}
