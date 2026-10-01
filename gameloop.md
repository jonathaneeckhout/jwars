# Jwars game loop

## Purpose

Jwars is a week-long, free-for-all RTS competition for 5–10 players. Players write and improve autonomous clients, then compete to control a shared objective. The appeal is both strategic and technical: each client must make good decisions from partial information, coordinate units, and adapt to rival clients over a persistent match.

## Season format

- One shared match runs for seven real-time days.
- The world continues running throughout the season; players' clients can operate without the player being online.
- The season ends after seven days. The player with the highest score wins. A tie is a shared win.
- At the next season, the map and score reset for a new competition.
- The MVP is free-for-all. Other players are the only opponents; there are no neutral threats or scheduled PvE events.

The season starts when the world is first initialized. At the end of a season, the server records the final standings and winners, resets the map and score, and starts the next seven-day season while retaining player accounts and API tokens.

## Core loop

Players and their clients repeat this loop throughout the season:

1. **Observe:** Read the player-visible snapshot and ordered world events. Enemy entities are visible only while within the player's vision.
2. **Develop:** Expand the base and build up the forces needed to contest the objective. Economy and military strength help a player compete, but do not directly add to the score.
3. **Scout and maneuver:** Use unit movement and limited vision to locate opponents, choose routes, and position forces.
4. **Fight for the hill:** Give soldiers attack and movement orders to take or defend the single fixed hill at (500, 500). Its control area is a five-tile Chebyshev radius (a square). Workers do not control the hill.
5. **Earn control points:** A player earns one point per 60 world ticks (currently one second per tick) while their soldiers control the hill uncontested. Rival soldiers in the control area make it contested, so nobody scores while it is contested.
6. **Adapt:** React to events, changes in visibility, attacks, and opponents' attempts to take control. A client should make frequent tactical decisions; the player should not need to issue every order manually.

Control points are banked permanently. Losing the hill stops future scoring but does not remove points already earned. The score is the total control points earned over the full seven-day season.

## Client and server responsibilities

- The server is authoritative for world state, visibility, movement, combat, hill control, score, season end, and winner determination.
- A client observes state through snapshots and ordered events, then submits commands such as move and attack. The server validates and resolves those commands.
- Clients are expected to run autonomously and respond to changing state. The strategic challenge should come from scouting, unit coordination, choosing when to fight or retreat, and adapting to opponents.
- Keep the action set understandable. Avoid making success depend on a large menu of special commands; agent strategy should provide most of the complexity.

## Scoreboard

The scoreboard tracks each player's banked control points during the season. At season end, the highest score wins; tied highest scores share the win. Resources and unit counts are not alternate score categories or tie-breakers in the MVP.

## Deferred design decisions

These are intentionally left for a later pass and must not be silently assumed while implementing the core loop:

- How players enter a season and where their bases start.
- How players gather resources and create or replace units.
- What happens when units die and how a player recovers from losses.
- Whether future scoring updates should be emitted more frequently than point milestones.

## MVP acceptance behavior

- A season has one fixed hill, one cumulative control-point score per player, and a seven-day end time.
- Only soldiers can establish and maintain hill control.
- A player scores while they are the only player's soldiers in the hill's control area; presence by rival soldiers contests it and pauses scoring.
- Points remain banked after control changes hands.
- The scoreboard identifies the highest score and supports shared wins on a tie.
- The world can continue the loop while a player's autonomous client operates without manual per-order input.
- `GET /v1/scoreboard` and the authenticated world snapshot expose the active season, hill state, and standings/own score.
- Hill ownership changes and each newly earned point are delivered as ordered events; no per-tick score event is emitted.
