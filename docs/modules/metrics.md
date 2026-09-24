# Metrics

`cs2node top` is the view you actually want on a node full of CS2 servers: who is playing what, and what it costs.

```
 cs2node top  game2  12:50:11                                        cs2node 1.0.0 stable
 CPU [||||||||||||                  ]  42.0%  16 cores   MEM [||||||||          ] 6.1G/16G
 servers 3   players 27   egg 3/3   sort cpu   refresh 1s

 SERVER                          MAP           PLAYERS       CPU               MEMORY     RX/s     TX/s  EGG
 59e9667c  Retake #1            de_dust2           12  [||||||  ]  84.2%  [|||   ]  1.9G/4.0G   210K     1.4M  ok
 7c1d0a2e  Surf | k4ryuu.com    de_mirage           0  [        ]   3.1%  [||    ]  1.1G/4.0G     2K       3K  ok
 q quit  s sort  ↑↓ scroll
```

Keys: `q` quits, `s` cycles the sort between cpu, players, memory and name, arrows and PageUp/Down scroll. The layout follows the terminal width: wide terminals get the bars, narrow ones drop them, then the network columns, then memory. Resizing redraws.

The first column is the short server id and the name players see. The id is what every command takes, and any prefix of it works, so what you read you can paste. The name is the `hostname` convar: the egg asks the server for it on every map load, because a plugin may change it per map, and falls back to what `server.cfg` sets.

The numbers come from docker's own stats stream, the same feed `docker stats` reads, so a one second refresh costs nothing extra. Memory excludes the page cache, the way `docker stats` reports it. Map and player counts come from the egg; a `-` means it has not reported yet.

## Prometheus

The same numbers on `http://127.0.0.1:9151/metrics`:

| Metric | Labels |
|---|---|
| `cs2node_info` | `version`, `channel` |
| `cs2node_uptime_seconds`, `cs2node_servers` | |
| `cs2_server_up`, `cs2_server_egg_connected`, `cs2_server_players` | `server` |
| `cs2_server_map_info` | `server`, `map` |
| `cs2_server_cpu_percent`, `cs2_server_memory_bytes`, `cs2_server_memory_limit_bytes`, `cs2_server_pids` | `server` |
| `cs2_server_network_receive_bytes_total`, `cs2_server_network_transmit_bytes_total` | `server` |

`listen` defaults to `127.0.0.1:9151`. Put it on `0.0.0.0` only behind a firewall.

---

[Docs index](../README.md)
