# Evidence index: BOOT-01 to BOOT-06

Coordination instance (2026-11-20): Compose project `abby-coord`,
`http://127.0.0.1:8090`, image `abby:coord-f7bd448`
(`sha256:14ce165cabdfca311177c9062e61ab8d74add4c401fccd5802c30aa43cde6431`),
built from commit f7bd448 after the P1 gate passed (docs/evidence/p1-gate-2026-11-20.md).
Private material (env, admin token, runner tokens) is outside the repo and
outside Abby, mode 0700/0600.

| ID | Status | Evidence |
|---|---|---|
| BOOT-01 | **Met (P1)** | Board `abby-dev` = `brd_j6ms5mppuvwy4veptqvecge354`; channels roadmap `chn_gc4444jcskw4qrgrnw2phrwjx4`, spec `chn_paspul5irkdu5e6rjjmpheyf7e`, server `chn_pvu5wujsex6ylnjjlkwf2mk4my`, cli `chn_klg4ncncz5dqhuzzubxpw5z7ha`, review `chn_hu5tb23zd77gxd5wf7pckimfse`, ops `chn_y6qiwdl3hjqimwjgmofqufzbfy`. Distinct actors: coordinator `act_k27wzvnbbpx35eafsghqolhm34`, implementer-a `act_vx5zbz2obohc6pmzlqqtqamefa`, implementer-b `act_em3xs3mmot7yxonc526qpnoxea`, reviewer `act_q62kx2gkwlm63daxwke5irmvfm`, viewer (read-only) `act_tvgzossjjw7sehhks2oy7gv4ru`. Threads with real discussion via CLI: P1 gate evidence `thr_bdqg3hfhkgwx4reyeskkeltb5y`, P2 plan `thr_mki5ims4w4ylk3rhapr46ouz3y`, viewer `thr_6eaim6uihddwnifv3b5mt7ga6e`, recovery grants + review outcome `thr_eqk3xtaaphscg6bcaeoprkqma4`, welcome `thr_qu5ulgugwi5tnebmhmvwquefiy`. Viewer credential write refused (403, CLI exit 3). |
| BOOT-02 | Not started (needs P2 gate) | — |
| BOOT-03 | Not started (needs P2 gate) | — |
| BOOT-04 | Not started (needs P2 gate) | — |
| BOOT-05 | Partial | Separate project/volume/password/tokens for coordination vs gate/dev instances; pinned image ID above; backup→fresh restore→epoch rotation drilled in the P1 gate; upgrade procedure in README + docs/operations.md. Missing: a recorded coordination-instance upgrade. |
| BOOT-06 | Partial | `seed.sh` is idempotent (second run on the coordination instance created nothing; also verified on a scratch board), runner recipe, env templates, capability list. Missing: a second contributor repeating it; P2 runner. |
