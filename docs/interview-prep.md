# Gemline — fiche de préparation entretien

> Fiche rédigée le 1er septembre 2026 à partir d'une relecture complète du dépôt (code, migrations, infra, historique git). Elle couvre **les deux versions du CV** : l'ancienne (état de juin 2026 : backplane Postgres `LISTEN/NOTIFY`) et l'actuelle (état depuis le 30 juillet 2026 : single-writer + leases + Redis). Les références `fichier:ligne` pointent dans le dépôt ; relis-les si on te pousse sur un détail.
>
> Complément : `docs/architecture-notes.md` (notes système design, « pourquoi pas Kafka », architecture cible).

---

## 0. Le pitch

### 30 secondes

« Gemline est un jeu de plateau multijoueur temps réel — 2 à 6 joueurs sur une grille hexagonale — que j'ai construit seul de bout en bout et qui tourne en production sur `gemline.werilo.fr`. Backend Go en event sourcing : l'historique des coups est la source de vérité, l'état est reconstruit par rejeu déterministe. Le backend tourne en plusieurs replicas sur un cluster Kubernetes k3s auto-hébergé chez Hetzner, provisionné par Terraform et Ansible, déployé en GitOps avec ArgoCD, avec une stack d'observabilité complète. »

### 2 minutes (ajouter selon la version du CV)

**Version actuelle** : « Pour scaler horizontalement sans casser la correction du jeu, chaque partie live est possédée par exactement un pod — un *lease* en Postgres avec TTL et heartbeat — et chaque écriture dans le journal est *fencée* par une époque : un pod zombie peut *croire* qu'il possède une partie, il ne peut plus *écrire*. Les événements sont diffusés via Redis pub/sub sur un canal par partie, donc un pod ne reçoit que ce que ses clients regardent. Le bus est volontairement *lossy* : les événements sont numérotés, le client détecte un trou et rattrape depuis le journal. J'ai aussi séparé le binaire en deux rôles, gateway (WebSockets) et gamesvc (logique), pour déployer la logique sans couper une seule connexion. »

**Version ancienne** : « Le multi-replica reposait sur un backplane Postgres `LISTEN/NOTIFY` : Postgres était la seule infrastructure et jouait quatre rôles — journal durable, bus pub/sub inter-pods, file de matchmaking (`FOR UPDATE SKIP LOCKED`) et offsets de rattrapage. Le contrat central, qui n'a jamais changé depuis : *le transport est lossy, la vérité est dans le journal, le client se resynchronise par offset*. » Puis enchaîner : « Depuis, j'ai fait évoluer cette architecture : … » (voir §1.3).

**Côté infra (les deux versions)** : « Cluster k3s en HA — 3 control planes avec etcd embarqué derrière un Load Balancer Hetzner — provisionné par Terraform (VMs, réseau privé, firewall, LB, DNS Cloudflare, état distant S3 + verrou DynamoDB), configuré par Ansible avec un inventaire dynamique lu depuis les outputs Terraform, et piloté par ArgoCD en app-of-apps. La CI construit les images, commit le bump de tag dans le repo, et ArgoCD fait le rollout depuis l'intérieur du cluster : la CI n'a jamais de kubeconfig. Les secrets viennent d'AWS Secrets Manager via External Secrets, authentifié par OIDC — l'équivalent d'IRSA reconstruit sur k3s — donc zéro clé cloud longue durée dans le cluster. »

### Ce que le projet démontre (à placer)

- Capacité à **identifier un bug de correction distribué** (timers dupliqués entre replicas → double forfait) et à le résoudre avec les bons patterns (lease + fencing), pas avec un hack.
- **Discipline d'invariants** : moteur pur (100 % de couverture, gate CI à 95 %), `now` injecté partout, protocole client stable à travers deux refontes du transport.
- **Ops réels** : cluster reconstruit plusieurs fois, incidents documentés dans les commits, alertes et dashboards as code.
- **Honnêteté sur l'échelle** : « c'est sur-architecturé pour le trafic réel, et je l'assume : chaque brique a été introduite pour un bug concret ou pour apprendre un concept précis, et je sais dire lequel des deux designs déployer selon le contexte. »

---

## 1. Chronologie et les deux versions du CV

### 1.1 Timeline (239 commits, 11 mai → 16 août 2026)

| Date | Étape |
|---|---|
| 11 mai | Moteur de règles + serveur + React + persistance event-sourced + Supabase auth (5 commits le premier jour) |
| 12–15 mai | Docker, k8s, Terraform Hetzner, ArgoCD, premier déploiement ; PR #11 : **backplane LISTEN/NOTIFY** pour le multi-replica |
| 20 mai | Monitoring, Sealed Secrets → puis ESO + Infisical (saga de 7 commits sur le Gateway Infisical) |
| 22 mai | CI Terraform (plan en PR / apply gaté) ; **HA k3s en 4 phases** (PR #35–#38, `cp_count` 1 → 3) |
| 30 mai | **Migration vers AWS Secrets Manager + OIDC « IRSA-equivalent »** (`ef7aa9c`), Sealed Secrets et Infisical supprimés |
| 5–7 juin | Loki + Alloy, pod-identity-webhook, Tempo, OpenTelemetry dans le backend, dashboards et alertes as code |
| 20 juin | Polish pour revue (PR #43) — **état de l'ancien CV** |
| 30 juillet | PR #44 `feat/game-affinity` : leases → fencing → forward → Redis par partie → suppression LISTEN/NOTIFY → split gateway/gamesvc → élection de leader. PR #45 : propagation des traces — **état du nouveau CV** |
| 16 août | README de présentation, screenshots, MIT |

### 1.2 Ce qui est vrai dans l'ancien CV (état de juin) — et comment en parler

| Phrase du CV | Réalité en juin | Ce qu'il faut savoir dire |
|---|---|---|
| « scaling horizontal multi-pod via backplane Postgres LISTEN/NOTIFY » | Vrai : 2 replicas, 2 canaux globaux `gemline_events` / `gemline_lobby`, invalidation de cache cross-pod | Comment ça marchait, ses limites (§3), et **ce que tu as fait depuis** |
| « Cluster k3s auto-hébergé en HA » | Vrai depuis le 22 mai : 3 CP + etcd embarqué + LB Hetzner | Quorum impair, `--cluster-init` vs `--server`, `serial: 1` |
| « chaque git push déclenche un déploiement zéro-downtime » | Vrai : ~3 min du push au live, `maxSurge 1 / maxUnavailable 0` | Le chemin CI → GHCR → commit `[skip ci]` → ArgoCD |
| « OIDC AWS↔Kubernetes, équivalent IRSA self-hosted » | Vrai depuis le 30 mai | Le flux complet (§5.5) — c'est *le* sujet où l'on peut te creuser |
| « Stack complète d'observabilité » | Vrai depuis le 7 juin | RED, cardinalité, traces, `tracesToLogsV2` |

Si l'interviewer a lu cette version : **ne pas faire semblant que rien n'a changé**. Formulation : « Depuis l'envoi du CV j'ai continué : le backplane LISTEN/NOTIFY a été remplacé par un modèle single-writer avec leases et fencing, et Redis routé par partie. Je peux vous expliquer les deux et pourquoi j'ai migré. » C'est un *atout* (tu montres l'itération), pas une contradiction.

### 1.3 Ce qui a changé entre les deux (à réciter)

1. **Affinité partie → pod** : leases Postgres (TTL 15 s, heartbeat 5 s), époque de fencing sur chaque append.
2. **Forward des commandes** vers le pod propriétaire (HTTP `ReverseProxy`, chaîne de repli owner → pool → local).
3. **Bus Redis routé** `game:{id}` avec abonnements refcountés ; LISTEN/NOTIFY supprimé.
4. **Split gateway / gamesvc** : un binaire, `ROLE=all|gateway|gamesvc`.
5. **Élection de leader** pour le matchmaker + « doorbell » Redis à l'enqueue.
6. **Propagation des traces** à travers le forward (une seule trace gateway → owner → SQL).

Ce qui n'a **jamais** changé : le moteur pur, le protocole client (`state` snapshot + flux `seq` + `/events?since=`), le schéma du journal, le replay, les seat tokens. → **Zéro modification frontend** pendant la refonte.

---

## 2. La stack — et pourquoi chaque choix

### 2.1 Backend

| Choix | Pourquoi | Alternative écartée |
|---|---|---|
| **Go** | Concurrence simple (goroutine par connexion WS, `select` sur timers/canaux), binaire statique ~15 MB pour du distroless, `-race` en CI, stdlib HTTP suffisante (`net/http` + `ServeMux` 1.22+ avec patterns), démarrage instantané pour des rollouts rapides | Node (moins bon pour les timers/CPU du bot), Rust (boucle de dev plus lente pour un solo) |
| **Event sourcing** (`moves` = vérité, état rebâti par `ApplyMove`) | Pas de dérive entre état stocké et règles : les captures sont *re-exécutées* par le même moteur au chargement ; replay gratuit pour la fonctionnalité « revoir la partie » ; audit complet | Snapshot d'état JSON (risque de divergence règles/état, pas de replay) |
| **Moteur de règles pur** (`internal/game`, zéro I/O, `now` en paramètre) | Testable à 100 %, déterministe, réutilisé par le serveur, le replay ET le bot (`ai`) | Règles mélangées au serveur |
| **PostgreSQL** (via Supabase en prod) | Transactions, verrous de ligne (séquence monotone gratuite), `FOR UPDATE SKIP LOCKED` (file de jobs), `NOW()` comme horloge partagée pour les leases, JSONB pour les payloads. Une seule brique pour journal + leases + comptes + file | MongoDB (pas de verrous de ligne exploitables de la même façon), Redis comme store (pas de durabilité voulue) |
| **pgx v5 via `database/sql`** (pas `pgxpool`) | Nécessaire pour **otelsql** (traces SQL) et **goose** (migrations embarquées `//go:embed`, appliquées au démarrage) | `pgxpool` (plus rapide mais pas de wrapping OTel standard) |
| **Supabase Auth** | Auth email/mdp gérée, JWT signés vérifiés côté Go via **JWKS** (asymétrique, pas de secret partagé) ; le backend ne stocke aucun mot de passe. Base Postgres incluse (free tier) | Auth maison (surface de risque inutile pour un solo) |
| **Redis pub/sub** (version actuelle) | Canaux jetables à fort churn (`game:{id}`), `SUBSCRIBE`/`UNSUBSCRIBE` dynamiques, re-abonnement auto de go-redis, nettoyage automatique à la déconnexion. **Sans persistance** (`--save "" --appendonly no`) : il ne porte que du fan-out lossy | NATS (aurait marché aussi), Kafka (voir §6 « pourquoi pas Kafka ») |
| **Postgres LISTEN/NOTIFY** (version ancienne) | Zéro brique supplémentaire ; transactionnel par nature ; suffisant pour 2 pods | Voir §3 pour ses limites structurelles |
| **coder/websocket** | Lib WS moderne, compatible `context`, ping/pong protocolaire | gorilla/websocket (archivé un temps) |
| **`golang.org/x/time/rate`** | Token bucket stdlib-adjacent, par appelant | Middleware tiers |
| **Bot : negamax profondeur 2 + α-β** | Suffisant pour « remplir un siège », déterministe (seed), borné (60/20 candidats) | MCTS (sur-dimensionné) |

### 2.2 Frontend

| Choix | Pourquoi |
|---|---|
| **React 19 + Vite 8 + TypeScript + Tailwind 4** | Boucle de dev rapide, code splitting `lazy/Suspense` par route, Tailwind v4 sans config PostCSS |
| **SVG inline** pour le plateau | 331 intersections, hit-testing natif, zoom vectoriel, accessibilité (`role="img"`) |
| **WebSocket singleton par partie** + `seq` + catch-up | Un seul socket partagé entre composants, reconnexion exponentielle avec jitter ±30 %, détection de trou de `seq` → `GET /events?since=` |
| **Caddy** pour servir la SPA | Image ~25 MB, `try_files` SPA, compression zstd/gzip, cache immutable sur `/assets/*` |
| **PWA (manifest)** | Add-to-home-screen mobile ; pas de service worker (pas d'offline) |

### 2.3 Infra

| Choix | Pourquoi | Alternative écartée |
|---|---|---|
| **Hetzner Cloud** | ~23 €/mois pour 4 VMs `cx23` (2 vCPU/4 GB) + LB ; réseau privé ; API Terraform mature | AWS EKS (~70 $/mois rien que pour le control plane), Scaleway |
| **k3s** | Kubernetes conforme mais léger (un binaire, SQLite/etcd embarqué, Traefik inclus) ; idéal pour 4 nœuds à 2 vCPU | kubeadm (plus lourd à opérer), k0s |
| **HA : 3 control planes, etcd embarqué** | Quorum Raft = majorité ⇒ nombre impair ≥ 3 ; tolère la perte d'un CP ; workers et `kubectl` passent par le LB | 1 CP (moins cher, mais SPOF) |
| **Terraform** | Déclaratif, plan reviewable en PR, état distant S3 + verrou DynamoDB, providers hcloud/cloudflare/aws | Pulumi, scripts |
| **Ansible** | Configuration des VMs (k3s, cert-manager, ArgoCD) idempotente ; inventaire dynamique depuis `terraform output -json` — pas d'IP à maintenir | cloud-init seul (pas de ré-exécution), Terraform `remote-exec` (fragile) |
| **ArgoCD (GitOps pull)** | Le cluster tire son état depuis `main` : `automated` + `prune` + `selfHeal`, **la CI ne touche jamais au cluster** (pas de kubeconfig dans GitHub), rollback = `git revert` | Flux (équivalent), `kubectl apply` en CI (push, secrets cluster dans la CI) |
| **App-of-apps + Kustomize** pour l'app, **Helm** pour les composants tiers | Kustomize suffit pour des manifests maison (`kustomize edit set image` en CI) ; Helm pour les charts upstream (kube-prometheus-stack, Loki…) | Helm pour l'app (templating inutile) |
| **GHCR** | Gratuit pour images publiques, pas de pull secret, login via `GITHUB_TOKEN` | Docker Hub (rate limits) |
| **cert-manager + Let's Encrypt HTTP-01 via Traefik** | TLS auto-renouvelé ; HTTP-01 impose `proxied = false` côté Cloudflare et le port 80 ouvert | DNS-01 (aurait permis le proxy Cloudflare) |
| **AWS Secrets Manager + External Secrets Operator + OIDC** | Valeurs hors du repo et hors du state Terraform ; survivent à la destruction du cluster ; zéro clé longue durée : ESO échange son token de ServiceAccount contre des credentials STS courts | Sealed Secrets (clé maître perdue lors d'un rebuild — vécu), Infisical (chaîne à 3 couches, bootstrap secret nécessaire), Vault (trop lourd) |
| **kube-prometheus-stack + Loki + Tempo + Alloy** | Standard de facto ; Grafana unique ; Loki/Tempo en mode mono-binaire avec stockage S3 (cycle de vie 60 j en filet de sécurité) ; Alloy en DaemonSet collecte logs + reçoit OTLP | Datadog (coût), ELK (lourd) |
| **OpenTelemetry** | Vendor-neutral, propagation W3C `traceparent`, `otelhttp` + `otelsql` ⇒ une trace gateway → owner → SQL | Tracing propriétaire |
| **Distroless `static:nonroot`** | Pas de shell, pas de libc, UID 65532, `readOnlyRootFilesystem`, `drop ALL` | Alpine (shell présent) |

---

## 3. Architecture — version ancienne (LISTEN/NOTIFY), à maîtriser pour l'entretien « ancien CV »

### 3.1 Vue d'ensemble

Monolithe Go × 2 replicas derrière Traefik, **pas de sticky sessions** : n'importe quel pod traite n'importe quelle requête. Chaque pod garde un cache mémoire des parties (`map[gameID]*GameRecord`), cohérence par **invalidation** cross-pod. Postgres jouait quatre rôles :

1. **Journal durable** : `game_events(game_id, seq, type, payload JSONB)`, append-only, `seq` monotone par partie.
2. **Bus pub/sub** : `LISTEN/NOTIFY`, deux canaux globaux `gemline_events` et `gemline_lobby`.
3. **File de matchmaking** : `matchmake_queue` + `FOR UPDATE SKIP LOCKED` (chaque pod tirait des lots disjoints — *competing consumers* sans broker).
4. **Offsets de rattrapage** : `GET /events?since=N`.

### 3.2 Cycle de vie d'un coup (ancienne version)

1. Le client envoie `POST /moves` (ou WS) → un pod quelconque.
2. Le pod charge la partie (cache ou replay depuis `moves`), valide via le moteur, persiste le coup (`AppendMove`), puis `AppendEvent` :
   ```sql
   WITH s AS (UPDATE games SET event_seq = event_seq + 1 WHERE id = $1 RETURNING event_seq)
   INSERT INTO game_events (game_id, seq, type, payload) SELECT $1, s.event_seq, $2, $3 FROM s RETURNING seq;
   ```
   Le **verrou de ligne sur `games.id`** sérialise les écrivains concurrents : séquence monotone sans verrou explicite.
3. `SELECT pg_notify('gemline_events', '{"gameId","seq","podId"}')` — **payload = identifiants seulement** (cap NOTIFY ~8 Ko), via le pool, **hors de la transaction d'insert** (deux aller-retours ; d'où le contrat lossy).
4. Chaque pod possède **une connexion pgx dédiée** en `LISTEN` (session-scoped, hors pool), boucle `WaitForNotification`, reconnexion toutes les 2 s en cas d'erreur. À réception : si `podId ≠ moi` → invalider le cache ; si j'ai des abonnés WS sur cette partie → `LoadEvent(gameId, seq)` en base → push WS.
5. Le client compare `seq` : trou → `GET /events?since=lastSeq`.

### 3.3 LISTEN/NOTIFY en 90 secondes (à réciter)

- **Transactionnel** : la notification part au `COMMIT` (jamais sur rollback).
- **Session-scoped** : `LISTEN` est un état de session → connexion dédiée longue durée ; `NOTIFY`/`pg_notify` passe par le pool.
- **Asynchrone** : le message arrive sur la socket hors cycle requête/réponse.
- **At-most-once** : abonné déconnecté = message perdu ; pas de rétention, pas de replay, payload ≤ 8 Ko.
- **Un seul flux par pod** : handlers séquentiels sur la connexion listener.

### 3.4 Les vraies limites (celles qui ont motivé la migration)

1. **Timers non coordonnés — le vrai bug.** Pendules (flag), forfait sur déconnexion (60 s), coups de bot étaient des `time.Timer` locaux à chaque pod : tout pod qui *cachait* une partie armait *son* timer ⇒ **double forfait, coups de bot dupliqués**. Problème de correction, pas de charge.
2. **Broadcast global** : 2 canaux pour tout le site, chaque pod reçoit et filtre 100 % du trafic. Coût par pod ∝ trafic global.
3. **Rate limiting per-pod** : limite effective = limite × replicas.
4. **Cohérence par invalidation** : un aller-retour Postgres par coup et par pod pour recharger.
5. **Jeu d'abonnements figé par session** : nouveaux canaux seulement après reconnexion ⇒ incompatible avec un canal par partie.
6. Deux bugs de prod typiques de ce modèle : `caddc3b` (lost update sur l'acceptation de rematch, corrigé par `SELECT … FOR UPDATE`) et `ee19e73` (`draw_offer_by` en mémoire seulement → 409 intermittent selon le pod).

> Phrase clé : « Ces problèmes appellent un *lease distribué* et un fan-out routé — pas une plateforme de streaming. »

### 3.5 Pourquoi pas Kafka dès le début

Postgres offrait déjà les quatre propriétés d'un broker (log ordonné, offsets, pub/sub, competing consumers) **plus** la transactionnalité. À 2 pods de 50m CPU, un cluster Kafka serait plus lourd que l'application, et introduirait un second système de vérité (dual-write). Signaux qui le justifieraient : fan-out massif (milliers de spectateurs), N consommateurs découplés (anti-triche, analytics), volume d'écriture > une instance Postgres. Aucun n'est atteint. `game_events` reste l'interface naturelle pour un CDC/outbox le jour venu.

### 3.6 Le texte « pourquoi j'ai arrêté LISTEN/NOTIFY » (à dire tel quel)

Au départ, Postgres faisait tout : il stockait l'historique des coups, et il servait aussi de « haut-parleur » entre mes deux pods grâce à LISTEN/NOTIFY. Quand un pod enregistrait un coup, il criait « partie X, événement n°42 » sur un canal global, et tous les pods l'entendaient. Ceux qui avaient des joueurs connectés sur cette partie allaient relire l'événement en base et le poussaient en WebSocket. C'était simple, sans composant supplémentaire, et ça a tourné correctement en prod pendant deux mois.

Le problème n'est pas venu de la charge. Il est venu de la correction du jeu. Avec ce modèle, n'importe quel pod peut traiter n'importe quelle partie : aucun pod n'est « responsable » d'une partie donnée. Or une partie a des choses qui se passent toutes seules, sans action du joueur : la pendule qui tombe, le forfait après 60 secondes de déconnexion, le coup du bot. Ces timers vivaient en mémoire dans chaque pod. Résultat : dès que deux pods avaient la même partie en cache, chacun armait son propre timer, et on se retrouvait avec un double forfait ou un coup de bot joué deux fois. J'ai eu deux autres bugs de la même famille : une acceptation de rematch écrasée parce que deux pods écrivaient en même temps, et une offre de nulle qui donnait un 409 une fois sur deux selon le pod qui répondait. Le symptôme typique, c'était « réessaie, ça marche ».

À ce moment-là, j'ai compris que le vrai manque n'était pas un meilleur bus, c'était un propriétaire unique par partie. LISTEN/NOTIFY avait aussi ses limites propres qui allaient dans le même sens : les canaux sont globaux, donc chaque pod reçoit et filtre tout le trafic du site. La liste des canaux écoutés est figée par connexion, donc impossible de s'abonner à « la partie X » à la volée. Et c'est du at-most-once, sans rétention : un pod déconnecté une seconde a raté le message. Ce dernier point, je l'avais déjà accepté en numérotant les événements, mais les deux premiers bloquaient la direction que je voulais prendre.

La nouvelle version repose sur deux idées. D'abord, chaque partie en cours appartient à exactement un pod. Ce pod prend un « lease » dans Postgres, un bail avec une durée de vie de 15 secondes qu'il renouvelle toutes les 5 secondes. Si le pod meurt, le bail expire et un autre pod reprend la partie. Chaque reprise incrémente une époque, et chaque écriture dans le journal vérifie cette époque : un vieux pod qui croit encore posséder la partie peut le croire, mais il ne peut plus écrire. Comme un seul pod possède la partie, il n'y a plus qu'un seul timer, donc plus de doublon. Les requêtes qui arrivent sur le mauvais pod sont simplement transférées au propriétaire.

Ensuite, le bus est passé sur Redis avec un canal par partie. Un pod ne s'abonne qu'aux parties qu'il possède ou que ses clients regardent, et se désabonne quand plus personne ne s'y intéresse. Le message transporté n'a pas changé : toujours « partie X, événement n°42 ». Et le contrat non plus : le bus a le droit de perdre un message, le journal en base reste la seule vérité, et le client qui voit un trou dans la numérotation va se rattraper en relisant depuis le dernier numéro connu.

En résumé : LISTEN/NOTIFY n'était pas un mauvais choix, c'était le bon choix pour démarrer sans infrastructure. Il a été remplacé parce que le vrai problème était « qui a le droit de faire vivre cette partie », et qu'un bus de diffusion ne peut pas répondre à cette question. Le remplacement a demandé zéro modification côté frontend, parce que le protocole client n'a jamais changé.

---

## 4. Architecture — version actuelle (single-writer + Redis)

### 4.1 Schéma

```
Browsers ──WS/REST──▶ gateway ×2 ──forward HTTP──▶ gamesvc ×2 (1 partie = 1 owner ; pendules, bots, matchmaker leader)
                        ▲                              │ append(seq, epoch)          │ PUBLISH game:{id}
                        │ SUBSCRIBE game:{id}          ▼                              ▼
                        └────────────────────── Postgres (journal, leases, comptes)   Redis (pub/sub, doorbell)
gateway ─JWKS─▶ Supabase Auth
```

### 4.2 Cycle de vie d'un coup (version actuelle)

1. Le client envoie le coup à **n'importe quel gateway**.
2. Le gateway résout le propriétaire : `SELECT owner_id, owner_addr FROM game_leases WHERE game_id=$1 AND expires_at >= NOW()` et **proxy en HTTP** (`httputil.ReverseProxy`) vers `POD_IP:8090`. Chaîne de repli : **owner → pool `gemline-gamesvc:8090` → traiter localement**. « Le routage a le droit de se tromper, l'écriture non. »
3. Le gamesvc propriétaire valide en mémoire, `seq++`, **append fencé** :
   ```sql
   WITH s AS (
     UPDATE games SET event_seq = event_seq + 1
     WHERE id = $1 AND ($4 = 0 OR EXISTS (SELECT 1 FROM game_leases l WHERE l.game_id = $1 AND l.epoch = $4))
     RETURNING event_seq)
   INSERT INTO game_events (game_id, seq, type, payload, epoch) SELECT $1, s.event_seq, $2, $3, $4 FROM s RETURNING seq
   ```
   Check d'époque et écriture **dans la même instruction, sous le même verrou de ligne** ⇒ un takeover entre le check et l'écriture est impossible. Zéro ligne + partie existante ⇒ `ErrStaleLease` : « je suis le zombie », on invalide son cache et on se tait.
4. Il réarme la pendule, publie `{gameId, seq, podId}` sur `game:{id}`.
5. **Seuls les gateways abonnés** (ceux qui ont un client sur cette partie) reçoivent, relisent l'événement en base, poussent en WS — y compris à l'auteur du coup, confirmé par le même chemin que les autres.
6. Trou de `seq` côté client → `GET /events?since=N`. **Inchangé depuis le monolithe.**

### 4.3 Leases (`internal/lease/lease.go`, migration `00017`)

```sql
CREATE TABLE game_leases (game_id TEXT PK, owner_id TEXT, epoch BIGINT, expires_at TIMESTAMPTZ, owner_addr TEXT);
```

- **TTL 15 s, heartbeat = TTL/3 = 5 s** (survit à deux heartbeats manqués). `owner_id = hostname-hex8`.
- **Acquire = un seul upsert à trois cas** : *libre* (insert, epoch 1), *déjà à moi* (renouvellement, epoch inchangé), *expiré* (takeover, `epoch + 1`). Un lease vivant tenu par un autre ne matche rien ⇒ la branche `UNION ALL` renvoie le détenteur. Les concurrents se sérialisent sur le verrou de ligne ⇒ un seul gagnant.
- **Horloge = celle de Postgres** (`NOW()`) partout : la dérive d'horloge entre pods est hors sujet.
- **Renew** : un seul `UPDATE … WHERE owner_id = $me RETURNING game_id` pour tous mes leases ; tout lease absent du `RETURNING` a été repris ⇒ `onLost(id)` (annuler pendule/présence, vider le cache, se désabonner). `Held()` est une *croyance* : « la correction vient du check d'époque à l'écriture, jamais de `Held()` ».
- **Release / Close** : `DELETE … WHERE owner_id = $me` ⇒ handover immédiat à l'arrêt propre ; l'expiration reste le filet pour la mort brutale.
- **Reprise** : par trafic (une commande arrive, `Acquire` réussit → `AdoptGame`) ou par le **sweeper d'orphelines** (toutes les 10 s, 20 parties `playing` sans lease vivant, les plus récentes d'abord ; tous les pods balaient, `Acquire` élit le gagnant). Après replay, la pendule est recalculée depuis les `played_at` persistés.
- **Epoch 0 = chemin dégradé volontaire** : mode mémoire, ou commande traitée localement sans lease ⇒ écriture non fencée, sérialisée par la base comme avant.

### 4.4 Ce que « single-writer » résout d'un coup

Modèle acteur : la mémoire du propriétaire *est* la vérité live ; coups traités séquentiellement ; **un timer = un propriétaire = zéro doublon** (`armClock` et `maybeScheduleBot` sont gatés sur `ownsGame()`, un seul gate couvrant tous les call sites). Limitation documentée : le timer de grâce sur déconnexion (60 s) reste sur le pod qui tient la WS (la présence est une connaissance locale à la connexion) — son écriture de résultat est fencée.

### 4.5 Bus Redis (`internal/bus/redis.go`)

- Canaux : `lobby`, `matchmake`, `game:{id}`. **Un seul `PubSub` partagé**, une goroutine de réception, handlers non bloquants.
- **Abonnements refcountés** : `refs[gameID]++` ; 0→1 = `SUBSCRIBE`, 1→0 = `UNSUBSCRIBE`. Deux sources d'intérêt : le cache du Store et les abonnés WS du Hub.
- Payload = `{gameId, seq, podId}` (héritage du cap NOTIFY — la migration Redis fut un changement de *routage*, pas de payload : la seam `Bus` a tout absorbé).
- Perte de message = non-événement : le client rattrape par `seq`. Redis sans persistance ; s'il meurt, go-redis se réabonne.
- **Doorbell** : `Enqueue` publie `"1"` sur `matchmake` → `WakeMatcher` fait un envoi non bloquant dans un canal à 1 slot (coalesce les rafales) ; le ticker 1,5 s reste le chemin de correction.

### 4.6 Split gateway / gamesvc (`cmd/server/main.go`)

Un binaire, `ROLE=all|gateway|gamesvc` (pattern Loki/Mimir « target »). Gateway : listener public `:8080`, `lease.Resolver` lecture seule, `SetForwardOnly()` ⇒ ne possède jamais de partie, pas de timers, pas de matcher. Gamesvc : listener interne `:8090` (jamais exposé par l'ingress), `lease.Manager`, elector, sweeper, `ADVERTISE_ADDR=$(POD_IP):8090`. Le tag « requête forwardée » vit dans le **contexte**, pas dans un header (non spoofable). Déployer la logique ne coupe **aucune** WS.

Arrêt propre : SIGTERM → `Shutdown` 10 s → elector resign → bus close → store close → `leases.Close()` (DELETE all, handover immédiat) → pool close. K8s : `preStop sleep 5`, `terminationGracePeriodSeconds 30`, `maxSurge 1 / maxUnavailable 0`.

### 4.7 Élection de leader (`internal/lease/elector.go`, `leader_leases`)

Même upsert à trois cas que les leases : **candidater = renouveler**. Ticker 5 s, TTL 15 s. `ErrNoRows` (un leader vivant ailleurs) ⇒ `stepDown()` annule le contexte de leadership (le workload — `StartMatcher` — meurt avec lui). Démission (`Close`) = DELETE immédiat ; crash = expiration ≤ 15 s, `epoch + 1`. Le `SKIP LOCKED` du tick rend l'éventuel bref chevauchement de deux leaders inoffensif.

### 4.8 Matchmaking

- File `matchmake_queue(user_id UUID PK, players, mode, rating, enqueued_at)` — PK sur `user_id` ⇒ un seul ticket par utilisateur.
- Tick : `SELECT … FOR UPDATE OF q SKIP LOCKED LIMIT 100` par bucket `(players, mode)`, appariement en Go pur, puis **dans la même transaction** : `INSERT games` (status `playing`, `public`), `INSERT seats` (réservés par identité, sans token), `DELETE FROM matchmake_queue`. Aucun état où l'utilisateur n'a ni partie ni ticket.
- 1v1 : bande Elo `100 + 10·âge_s`, plafond 1000 (~90 s) ; multi (3–6) : quorum 3, seuils d'attente 20 s / 10 s / 5 s / 0 s.
- Résultat poussé via `lobby` (`match_found {gameId}`) = **signal de navigation seulement** ; repli durable `GET /matchmake/current`. Le joueur arrive et **tire** son token de siège via `POST /seat/resolve` (JWT).

### 4.9 Auth et sécurité

- **Deux tokens orthogonaux** : JWT Supabase (`Authorization: Bearer`, *qui*), seat token (`X-Player-Token`, *quel siège dans quelle partie*). 24 octets `crypto/rand` (192 bits), **seul le SHA-256 est stocké**, comparaison en temps constant.
- JWT vérifié via JWKS (`keyfunc`), asymétrique. Faiblesse à assumer si on te la pointe : pas de pin explicite `iss`/`aud`/alg (single-tenant, mais `jwt.WithIssuer/WithAudience/WithValidMethods` serait mieux).
- Le middleware JWT **ne rejette jamais** : anonyme par défaut, `requireUser` → 401 là où il faut. Démarrage **fail-closed** si les comptes sont configurés mais injoignables (JWKS du fournisseur OIDC). La méthode de comptes vient de gamekit/auth (none, oidc, proxy, local), au choix de l'hébergeur ; entre pods, l'identité résolue par la gateway voyage dans un en-tête accepté seulement sur le listener interne.
- Backdoor de test supprimée (`c3bea05`) : `X-Test-User-ID` remplacé par un hook `var testAuthHook` nil en prod, câblé uniquement dans un `_test.go`.
- Corps ≤ 32 KiB, token bucket par appelant (per-pod, assumé), CORS strict + origin check WS, `/metrics` hors chaîne de middlewares.

---

## 5. Infra en détail

### 5.1 Terraform (`deploy/terraform/`)

- Providers : hcloud 1.63, cloudflare 4.52, aws 5.100, tls 4.3 ; backend S3 `gemline-tfstate-…` + DynamoDB `gemline-tfstate-lock`, `encrypt = true`.
- Ressources : `hcloud_network 10.0.0.0/16` + subnet `10.0.1.0/24` ; `hcloud_server.control_plane` (`count = cp_count`, validé ∈ {1,3,5,7}) et `workers` ; IPs privées `10.0.1.10+i` pour les CP, décalées de `cp_count` pour les workers ; firewall 22/80/443 ouverts, 6443 **seulement** si `kubeapi_allowed_ips` non vide ; **LB `lb11`** ciblant les CP par **label selector** (`role=control-plane`, IP privée) avec 3 services TCP passthrough (6443, 80, 443) ; DNS Cloudflare A record `ttl 60`, **`proxied = false`** (sinon HTTP-01 casse).
- `moved {}` blocks pour passer du singleton au `count` sans destroy (HA phase 1).
- `oidc.tf` : bucket S3 public sur exactement deux clés, `aws_iam_openid_connect_provider` (thumbprint TLS), rôle `gemline-eso-secrets-reader` avec trust `sub = system:serviceaccount:external-secrets:external-secrets`, `aud = sts.amazonaws.com`, policy `GetSecretValue` sur `gemline/env` et `gemline/grafana-admin` uniquement. `loki.tf` / `tempo.tf` : un bucket + un rôle IRSA par composant, cycle de vie 60 j.
- cloud-init ne fait *que* monter la NIC privée (`/32` + routes, car les réseaux Hetzner sont **routés, pas switchés**).

### 5.2 Ansible (`deploy/ansible/`)

- Inventaire **dynamique** `inventory.tf.py` : `terraform output -json` → groupes `control_plane`, `workers`, `k3s_cluster`, variable `lb_ipv4`.
- Playbook en 4 plays : `networking` → `k3s_server` avec **`serial: 1`** (le premier CP doit finir `--cluster-init` avant que les autres fassent `--server https://LB:6443`) → `k3s_agent` (join via le LB) → add-ons sur `control_plane[0]` uniquement (cert-manager v1.16.1 par manifest, ArgoCD v3.4.1 par manifest, Applications).
- Flags k3s importants : `--cluster-init` / `--server`, `--node-ip` et `--advertise-address` privés, `--flannel-iface=enp7s0`, `--tls-san=<IP publique>,<IP LB>`, **`--kube-apiserver-arg=service-account-issuer=<URL S3>`** et **`service-account-jwks-uri`** (le cœur de l'IRSA-equivalent). Token k3s dans **Ansible Vault**.
- Server-Side Apply avec `force_conflicts` pour cert-manager/ArgoCD (CRD ArgoCD > 256 Ko de l'annotation client-side).
- Idempotence : `creates:`, `stat`, `when:`, `changed_when`.

### 5.3 ArgoCD (`deploy/argocd/`)

App-of-apps de 9 Applications, toutes `automated {prune, selfHeal}` + `ServerSideApply`, ordre = ordre des CRDs : `external-secrets` (Helm 0.11.0) → `eso-config` (git) → `pod-identity-webhook` (Helm 2.6.5) → `monitoring` (kube-prometheus-stack 75.6.0, `ApplyOutOfSyncOnly`) → `loki` (6.18.0, SingleBinary, S3) → `tempo` (1.24.4, S3) → `alloy` (0.9.2, DaemonSet) → `monitoring-dashboards` (git) → `gemline` (git `deploy/k8s`, Kustomize).

À savoir : pourquoi ArgoCD v3 (v2 ne connaît pas `.status.terminatingReplicas` sur k3s ≥ 1.31 ⇒ statut de sync qui oscille) ; `disableNameSuffixHash` sur le ConfigMap des dashboards (sinon prune/recreate à chaque édition) ; `SkipDryRunOnMissingResource` sur les ServiceMonitors.

### 5.4 Manifests k8s (`deploy/k8s/`)

- `gateway` ×2 et `gamesvc` ×2 : `RollingUpdate maxSurge 1 / maxUnavailable 0`, `topologySpreadConstraints` soft (`ScheduleAnyway`) par hostname, probes `/healthz` (liveness) et `/readyz` (readiness), `preStop sleep 5`, requests `50m/64Mi`, limits `500m/256Mi`, `runAsNonRoot`, `runAsUser 65532`, `readOnlyRootFilesystem`, `drop ALL`.
- Services ClusterIP ; `gemline-gamesvc:8090` = pool par défaut, les forwards owner-addressed contournent le Service et **dialent l'IP du pod** lue dans la table de leases.
- Redis 1 replica sans persistance ; web (Caddy) ×3, `allowPrivilegeEscalation: false` seulement (le binaire porte `cap_net_bind_service`, drop ALL ⇒ EPERM à l'exec — vécu).
- Ingress Traefik : `/api`, `/ws`, `/healthz`, `/readyz` → gateway ; `/` → web ; TLS `gemline-tls` par `letsencrypt-prod`.
- Pourquoi `/readyz` renvoie toujours 200 : DB et Redis sont **fatals au boot** (`os.Exit(1)`), donc un process vivant est par construction connecté.

### 5.5 Secrets — le flux OIDC « IRSA-equivalent » (à savoir dérouler sans notes)

1. **Le cluster émet des JWT de ServiceAccount** signés par sa clé privée. Avec `--service-account-issuer=https://<bucket>.s3.eu-west-3.amazonaws.com`, le claim `iss` de ces tokens pointe vers une URL **publique**.
2. **`make publish-jwks`** copie `/.well-known/openid-configuration` et `/openid/v1/jwks` du cluster dans ce bucket S3 (public en lecture sur ces deux clés uniquement). AWS peut donc **valider la signature** des tokens du cluster comme n'importe quel IdP OIDC.
3. Terraform crée un **IAM OIDC provider** (trust de cet issuer, thumbprint TLS épinglé) et un **rôle IAM** dont la trust policy exige `sub = system:serviceaccount:external-secrets:external-secrets` et `aud = sts.amazonaws.com`.
4. Le ServiceAccount d'ESO porte l'annotation `eks.amazonaws.com/role-arn`. ESO lit un **projected token** (audience `sts.amazonaws.com`) et appelle **`sts:AssumeRoleWithWebIdentity`** → credentials **courts** (≈ 1 h), renouvelés automatiquement.
5. Le `ClusterSecretStore` (provider AWS, `auth.jwt.serviceAccountRef`) lit `gemline/env` dans Secrets Manager ; l'`ExternalSecret` (`dataFrom.extract`, refresh 1 h) matérialise un `Secret` K8s `gemline-env` consommé en `envFrom`.
6. **Pour Loki/Tempo** (SDK AWS vanilla, pas de chemin JWT propre), il a fallu installer le **`amazon-eks-pod-identity-webhook`** — le composant qu'EKS embarque par défaut — qui injecte `AWS_ROLE_ARN`, `AWS_WEB_IDENTITY_TOKEN_FILE` et le token projeté à l'admission. Sans lui, l'annotation est inerte (`NoCredentialProviders`, vécu).

Résultat : **aucune clé AWS longue durée** ni dans le cluster ni dans GitHub (la CI s'authentifie aussi en OIDC et lit `gemline/tf-vars` au runtime, masqué). Piège connu : ne pas mettre `provider.aws.role` en plus de l'annotation (double AssumeRole, le rôle ne se fait pas confiance à lui-même).

Pourquoi ce choix (historique réel) : Sealed Secrets ⇒ **clé maître perdue** lors du rebuild HA (#38), secrets committés irrécupérables ; Infisical ⇒ chaîne à 3 couches (SealedSecret → Infisical → ESO) et 7 commits de yak-shaving sur son Gateway ; AWS était déjà une dépendance (state + OIDC CI) ⇒ consolidation.

### 5.5 bis Le texte « pourquoi aucune clé longue durée » (à dire tel quel)

L'application a besoin de secrets : mot de passe de la base, clés Supabase. Loki, Tempo et la CI ont besoin d'accéder à AWS. La méthode classique, c'est une clé AWS collée dans un Secret Kubernetes ou dans GitHub. Le problème, c'est que cette clé ne meurt jamais. Si elle fuit, elle reste valable tant que personne ne la révoque.

J'ai d'abord essayé Sealed Secrets, qui chiffre les secrets dans Git avec une clé qui vit dans le cluster. Quand j'ai reconstruit le cluster pour la haute disponibilité, la clé est partie avec lui et mes secrets sont devenus illisibles. Leçon : les secrets doivent vivre hors du cluster. Ils sont donc dans AWS Secrets Manager.

Restait à prouver à AWS que le cluster a le droit de les lire, sans lui donner de clé. L'idée, c'est l'identité à la place du secret. Kubernetes émet déjà pour chaque composant un jeton signé qui expire au bout d'une heure. J'ai publié la clé publique du cluster sur Internet, déclaré le cluster comme émetteur de confiance dans AWS, et créé un rôle par composant : « seul ce ServiceAccount peut endosser ce rôle, et ce rôle ne lit que ces secrets ». Le pod présente son jeton, AWS vérifie la signature et le nom, et rend des credentials temporaires d'une heure. Rien n'est stocké.

C'est ce qu'EKS fait de base sous le nom IRSA. Ici, sur un cluster auto-hébergé, chaque pièce est posée à la main : la publication des clés, les flags du serveur API, le fournisseur d'identité, le webhook qui injecte le jeton dans les pods. La CI GitHub utilise le même mécanisme, GitHub étant lui aussi un émetteur reconnu par AWS.

Résultat : aucune clé longue durée, ni dans le cluster, ni dans Git, ni dans GitHub. Un jeton volé vaut une heure et les droits d'un seul composant. Un jeton forgé a une mauvaise signature.

### 5.6 CI/CD (`.github/workflows/`)

| Workflow | Déclencheur | Contenu |
|---|---|---|
| `test` | push main / PR | `go vet`, `go test -race -cover ./...`, **gate ≥ 95 % sur `internal/game`** (réel : 100 %) ; `npm ci`, lint, build |
| `deploy` | push main sur les chemins app | buildx + cache GHA, push GHCR `:sha` + `:latest`, `kustomize edit set image`, commit `deploy: bump images to … [skip ci]` (anti-boucle), `concurrency` sans annulation |
| `terraform-plan` | PR sur `deploy/terraform/**` | OIDC rôle plan (lecture), fmt/validate/tflint/**Trivy config** (HIGH/CRITICAL bloquant), plan posté en commentaire de PR, actions **SHA-pinnées** |
| `terraform-apply` | push main sur `deploy/terraform/**` | OIDC rôle apply, **environment `production` = approbation manuelle**, plan sauvegardé en artefact puis `apply tfplan.bin` (le plan *est* l'approbation), jamais annulable |

Chaîne complète d'un changement app : push → images → commit du bump → ArgoCD détecte (≤ 60 s) → rollout surge → **~3 min** push → live. Rollback : `git revert` ou `kustomize edit set image <sha>`.

**À assumer si demandé** : `test` et `deploy` sont deux workflows indépendants, déclenchés en parallèle par le même push. Les tests bloquent la PR (check rouge), **pas** le déploiement : un code qui compile mais dont les tests échouent est construit et déployé. Ce qui arrête un push cassé : le build (ne compile pas), la readiness (plante au boot, `maxUnavailable 0` garde l'ancien pod), le comparateur ArgoCD (manifeste invalide), puis les alertes (bug de logique). Fix connu, non fait : `workflow_run` / `needs` entre les deux, ou required check en branch protection.

### 5.7 Observabilité

- **Métriques** (`internal/server/metrics.go`) : `gemline_http_requests_total{method,route,status}`, `…_duration_seconds` (histogramme), `websocket_connections{hub}`, `games_created_total`, `games_finished_total{outcome}`, `moves_played_total{actor}`, `persist_errors_total{op}`. **Cardinalité maîtrisée** : `route` = pattern du mux (`GET /api/games/{id}`), jamais l'URL concrète ; `/ws/*` exclu (durée d'un handler upgradé = durée de la connexion) ; labels `op` sans id de partie/utilisateur.
- **Alertes as code** (3, app-layer) : `GemlineHigh5xxRate` (> 1 % sur 5 min, idiome `or vector(0)` / `clamp_min`), `GemlineHighRequestLatency` (p99 > 500 ms sur 10 min), `GemlinePersistErrors` (> 0 — la seule alarme sur les erreurs *avalées* par le Store). Alertmanager sans receiver réel (à dire si demandé).
- **Dashboard** « Gemline — Overview » : RED (rate/erreurs/durée), WS actives, parties finies par issue, coups/s humain vs bot, persist errors, logs d'erreur Loki.
- **Logs** : `slog` JSON → Alloy (DaemonSet, via l'API pods, sans hostPath) → Loki (SingleBinary, S3, rétention 30 j). Seuls 5 labels + `level` promu ; le reste se requête en `| json`.
- **Traces** : `otelhttp` (serveur + transport du proxy) + `otelsql` ; `patternSpanNamer` renomme les spans en `METHOD /pattern` ; export OTLP/HTTP vers Alloy → Tempo (S3) ; datasource Grafana avec `tracesToLogsV2` vers Loki. Une trace = gateway → proxy → owner → SQL.

---

## 6. Définitions à connaître absolument

### Distribué / données

- **Event sourcing** : l'état est une *fonction du journal* d'événements ; on ne stocke pas l'état, on le rejoue. Ici `moves` → `ApplyMove` ⇒ état. Avantages : audit, replay, pas de dérive état/règles. Coût : chargement = rejeu (ici ~200–400 coups max, négligeable ; sinon snapshots périodiques).
- **Replay déterministe** : même journal ⇒ même état. Conditions : pas de `time.Now()` dans le moteur (`now` en paramètre), pas d'itération de map, pas d'aléa. Test `TestPostgresRepo_CaptureSurvivesReload` : une capture (non stockée) est re-dérivée à l'identique.
- **CQRS** (si on te le demande) : séparation lecture/écriture ; ici léger — écriture par le propriétaire, lecture via snapshot + flux. Pas de projections séparées.
- **Single-writer / modèle acteur** : une entité (partie) = un seul processus qui la mute séquentiellement. Élimine structurellement les courses. Erlang/OTP, Akka, Orleans.
- **Lease** : verrou **qui expire** (TTL) ; le détenteur prouve qu'il est vivant en le renouvelant (**heartbeat**). Contrairement à un lock, un détenteur mort libère automatiquement.
- **Fencing token** (Kleppmann, « How to do distributed locking ») : nombre **strictement croissant** attaché à chaque acquisition ; le *stockage* rejette toute écriture portant un token périmé. Résout le cas « pause GC / partition : le détenteur croit encore tenir le lease ». Ici l'`epoch`, vérifié dans le `WHERE` de l'append.
- **Split-brain** : deux nœuds croient chacun être l'autorité. Prévenu ici par le fencing (le second ne peut pas écrire), pas seulement par le TTL.
- **Zombie owner** : ancien propriétaire qui n'a pas encore appris qu'il a perdu le lease. Il est rejeté à sa première écriture (`ErrStaleLease`).
- **Élection de leader** : choisir exactement un actif parmi N pour une tâche (matchmaker). Ici = un lease nommé ; candidater = renouveler.
- **At-most-once / at-least-once / exactly-once** : LISTEN/NOTIFY et Redis pub/sub = at-most-once (perte possible, jamais de doublon) ; Kafka = at-least-once (doublons possibles ⇒ consommateurs **idempotents**) ; exactly-once = at-least-once + idempotence/dédup. Ici les doublons WS sont dédupliqués par `seq` (`seq <= lastSeq` ⇒ ignoré).
- **Idempotence** : appliquer N fois = appliquer 1 fois. Ex. `UPDATE games SET rated_at = NOW() WHERE rated_at IS NULL` = garde « exactement une fois » pour l'Elo.
- **Pub/sub, fan-out, routage par intérêt** : l'émetteur publie sans connaître les abonnés ; fan-out = 1 événement → N destinataires ; routé = un canal par sujet pour que le coût d'un nœud soit ∝ ce que *ses* clients regardent.
- **Backpressure** : que faire quand un consommateur est lent. Ici : canal tamponné de 16 messages par connexion WS, envoi non bloquant, **drop assumé** (le client rattrape par `seq`) — le hub n'est jamais bloqué par un client lent.
- **Lost update** : deux lectures-modifications-écritures concurrentes, la seconde écrase la première. Corrigé par `SELECT … FOR UPDATE` (verrou pessimiste) — bug `caddc3b`.
- **`FOR UPDATE SKIP LOCKED`** : verrouille les lignes sélectionnées en sautant celles déjà verrouillées ⇒ file de jobs concurrente en SQL pur ; un crash mid-tick libère le verrou au rollback.
- **Verrou de ligne comme séquenceur** : `UPDATE games SET event_seq = event_seq + 1 … RETURNING` sérialise les écrivains d'une même partie sans verrou explicite.
- **CTE (`WITH … AS`)** : sous-requête nommée ; ici permet check + increment + insert en **une instruction atomique**.
- **Advisory lock Postgres** : verrou applicatif (pas lié à une ligne), session-scoped. Utilisé dans `internal/dbtest` pour sérialiser les paquets de tests intégration entre eux.
- **Outbox / CDC** : publier vers un broker *depuis* la transaction base (table outbox relayée, ou Debezium) pour éviter le **dual-write** (écrire en base ET publier = deux systèmes qui peuvent diverger).
- **Dual-write** : le problème que résout l'outbox ; ici accepté volontairement car le bus est lossy et la vérité est le journal.
- **Cache invalidation vs état chaud** : ancienne version = personne ne fait confiance à sa mémoire (invalidation + rechargement) ; nouvelle = le propriétaire fait confiance à sa mémoire (single-writer).
- **Elo** : `E = 1/(1+10^((Rb−Ra)/400))`, `R' = R + K(S−E)`, K = 32, départ 1200. Multi : adversaire « moyen », zéro-somme exact (reste arrondi sur le premier perdant).

### Kubernetes / infra

- **Control plane vs workers** : API server, scheduler, controller-manager, etcd (état du cluster) vs kubelet + runtime + kube-proxy qui exécutent les pods.
- **etcd, Raft, quorum** : store clé-valeur consensuel ; tolère ⌊(n−1)/2⌋ pannes ⇒ 3 nœuds tolèrent 1 perte ; nombre impair (4 nœuds ne tolèrent pas plus que 3).
- **k3s** : distribution Kubernetes certifiée, un binaire, etcd/SQLite embarqué, Traefik + ServiceLB (klipper) + local-path inclus. Ce n'est *pas* un Kubernetes réduit en API.
- **Deployment / ReplicaSet / Pod ; RollingUpdate `maxSurge`/`maxUnavailable`** : surge 1 / unavailable 0 = on ajoute avant de retirer ⇒ zéro perte de capacité.
- **Probes** : liveness (redémarrer si KO), readiness (retirer des endpoints du Service si KO), startup (optionnelle). `preStop` + `terminationGracePeriodSeconds` = fenêtre de drain.
- **Service ClusterIP / headless / Ingress / IngressClass** : IP virtuelle stable avec load-balancing (ClusterIP) ; headless = pas d'IP, DNS renvoie les pods ; Ingress = routage L7 HTTP(S) par un contrôleur (Traefik).
- **topologySpreadConstraints / anti-affinity / PDB / HPA** : répartir les pods entre nœuds ; PDB = borne de pods indisponibles pendant un drain ; HPA = autoscaling. (PDB/HPA absents ici — à dire si demandé.)
- **SecurityContext** : `runAsNonRoot`, `readOnlyRootFilesystem`, `capabilities.drop ALL`, `allowPrivilegeEscalation: false`. Distroless = pas de shell ⇒ surface réduite.
- **ServiceAccount, projected token, audience** : identité d'un pod ; token JWT signé par le cluster, monté en volume, avec `aud` ciblé et durée courte — la base de l'IRSA.
- **CRD / Operator** : extension de l'API (ExternalSecret, ServiceMonitor, PrometheusRule, Certificate) + contrôleur qui réconcilie.
- **Server-Side Apply, field manager** : l'API server fusionne et suit la propriété de chaque champ ; supprime la limite de 256 Ko de l'annotation `last-applied` client-side.
- **Admission webhook (mutating/validating)** : intercepte les objets avant persistance. Ex. ESO injecte des défauts (⇒ faux drift ArgoCD, `b14c4fe`) ; pod-identity-webhook injecte les env AWS.
- **GitOps** : Git = source de vérité déclarative ; un agent *dans* le cluster tire et **réconcilie** ; drift ⇒ `selfHeal` ; suppression dans Git ⇒ `prune`. Avantages : audit, rollback = revert, pas de credentials cluster dans la CI.
- **App-of-apps** : une Application ArgoCD racine qui déclare les autres ; ordre géré via la liste et les CRDs.
- **Kustomize vs Helm** : overlays/patches sans templating vs charts templatés paramétrés par `values`.
- **IaC, plan/apply, state, verrou** : Terraform compare l'état désiré (HCL), l'état connu (state S3) et le réel (API) ⇒ plan ; DynamoDB empêche deux applies concurrents ; `moved {}` = renommage sans destroy.
- **Idempotence Ansible** : ré-exécuter ne change rien si l'état est déjà atteint (`creates`, `when`, `changed_when`).
- **Inventaire dynamique** : script qui produit l'inventaire à la volée (ici depuis les outputs Terraform).
- **cloud-init** : premier boot d'une VM.
- **cert-manager / ACME / HTTP-01 vs DNS-01** : HTTP-01 prouve le contrôle du domaine en servant un token sur le port 80 (⇒ pas de proxy Cloudflare, port 80 ouvert) ; DNS-01 via un enregistrement TXT (permet wildcard et proxy).
- **Réseau Hetzner routé** : pas de broadcast L2 ⇒ `/32` + route explicite vers la gateway `10.0.0.1`.
- **Load balancer L4 passthrough** : le LB ne termine pas TLS ; Traefik le fait (cert-manager garde la main sur les certificats).

### Identité / sécurité

- **JWT** : header.payload.signature ; claims `iss`, `sub`, `aud`, `exp`. **JWKS** : jeu de clés publiques exposé à `/.well-known/jwks.json`, `kid` pour choisir la clé ; vérification asymétrique (RS256/ES256) sans secret partagé.
- **OIDC** : couche identité sur OAuth2 ; un issuer publie `/.well-known/openid-configuration` + JWKS ; n'importe quel *relying party* (AWS STS, GitHub → AWS) peut valider ses tokens.
- **IRSA** (IAM Roles for Service Accounts) : sur EKS, un pod assume un rôle IAM via son token de ServiceAccount (OIDC) — ici reconstruit à la main sur k3s : issuer S3 public + provider IAM + webhook.
- **STS `AssumeRoleWithWebIdentity`** : échange d'un JWT OIDC contre des credentials AWS temporaires.
- **Trust policy vs permission policy** : *qui* peut assumer le rôle (`sub`/`aud`) vs *ce que* le rôle peut faire (`GetSecretValue` sur `gemline/*`).
- **Moindre privilège** : rôles CI `gemline-tf-plan` (lecture + une seule valeur de secret) / `gemline-tf-apply` (écriture, scoped `gemline-*`).
- **Hash de token + comparaison en temps constant** : stocker `SHA-256(token)` ⇒ une fuite de base n'autorise pas l'usurpation ; `subtle.ConstantTimeCompare` évite les attaques par timing.
- **CORS / origin check WS** : liste d'origines exacte, `Vary: Origin` ; le WS n'est pas soumis à CORS ⇒ vérification d'origine explicite à l'upgrade.
- **Token bucket** : capacité (burst) + débit de remplissage ; ici par utilisateur ou IP (`X-Forwarded-For` préservé par le proxy), 429 + `Retry-After`.
- **Supply chain** : actions SHA-pinnées, `-lockfile=readonly`, Trivy config scan, images par digest SHA de commit.

### Observabilité

- **Trois piliers** : métriques (agrégats, alerting), logs (événements), traces (parcours d'une requête à travers les services).
- **Prometheus pull, scrape, ServiceMonitor** : Prometheus vient chercher `/metrics` ; le ServiceMonitor (CRD) déclare les cibles.
- **Counter / Gauge / Histogram**, `rate()`, `histogram_quantile()`, buckets `le`.
- **Cardinalité** : nombre de séries = produit des valeurs de labels ; un id de partie en label = explosion. D'où `route` = pattern, pas URL.
- **RED / USE** : Rate-Errors-Duration pour les services ; Utilisation-Saturation-Erreurs pour les ressources.
- **Alerte `for:`** : durée de persistance avant de déclencher (anti-flap).
- **OpenTelemetry** : API/SDK vendor-neutral ; **span** (opération avec début/fin/attributs), **trace** (arbre de spans), **contexte** propagé par le header W3C **`traceparent`** ; sampler `ParentBased(AlwaysSample)` par défaut ; export **OTLP** (gRPC 4317 / HTTP 4318) vers un collecteur (Alloy) puis Tempo.
- **Loki** : index sur les labels seulement (cheap), contenu greppé ; d'où peu de labels.
- **Tempo** : stockage de traces sur object storage, requêtes par trace id / TraceQL ; `tracesToLogs` pivote vers Loki.
- **Alloy** : agent Grafana (successeur de l'Agent) qui collecte logs et relaie OTLP.

### Go / réseau

- **Goroutine, canal, `select`, `context`** : timers = goroutine + `context.CancelFunc` par partie ; annulation en cascade à l'arrêt.
- **Mutex vs RWMutex** ; **race detector** (`-race`, activé en CI).
- **`http.Server.Shutdown`** : arrête d'accepter, attend les requêtes en vol jusqu'au timeout.
- **`httputil.ReverseProxy`** : proxy HTTP avec `ErrorHandler` (repli local seulement si aucun octet de réponse n'a été écrit).
- **WebSocket** : upgrade HTTP 101 ; **ping/pong** protocolaire (25 s ici) pour détecter les connexions **half-open** (TCP mort silencieux, proxies idle) ; le navigateur ne peut pas poser de header à l'upgrade ⇒ token en query.
- **Reconnexion exponentielle avec jitter** : `min(500ms·2^(n−1), 15s) × U(0.7, 1.3)`, 10 tentatives ⇒ évite le *thundering herd*.
- **Horloge : `time.Now()` vs horloge DB** : les comparaisons d'expiration se font en `NOW()` Postgres (une seule référence partagée) ; la seule comparaison horloge-locale/timestamp-stocké est celle des pendules, avec tolérance `elapsed < 0 ⇒ 0`.

---

## 7. Questions probables — réponses courtes

### Architecture & distribué

**Pourquoi l'event sourcing pour un jeu de plateau ?** Parce que l'état est *entièrement* dérivable d'une petite séquence de coups : stocker l'état serait redondant et risquerait de diverger des règles. Le replay donne gratuitement la reprise après crash, la fonction « revoir la partie » et un test d'équivalence puissant (replay == mémoire).

**Comment éviter deux propriétaires pour la même partie ?** Lease TTL + heartbeat pour le cas nominal, **fencing token** pour le cas dégradé : chaque lease porte une époque croissante, chaque append la porte, une écriture d'époque périmée est rejetée *dans la même instruction que le verrou de ligne*. Un zombie peut croire, pas agir.

**Le propriétaire crashe : que perd-on ?** Rien de durable. ≤ 15 s (TTL) puis le sweeper (10 s) ou le prochain trafic reprend la partie, rejoue le journal, recalcule la pendule depuis les `played_at` persistés, réarme les timers. On perd quelques secondes de latence, pas d'état.

**Que se passe-t-il si Redis tombe ?** Les gateways ne reçoivent plus les événements le temps de la reconnexion ; go-redis se réabonne ; les clients détectent le trou de `seq` et rattrapent. Redis n'a pas de persistance parce qu'il n'a rien à persister.

**Pourquoi le payload Redis ne contient que des identifiants ?** Héritage du cap NOTIFY à 8 Ko ; et ça garantit que ce qui est poussé est ce qui est *en base* (une seule forme pour le live et le catch-up).

**Pourquoi HTTP et pas gRPC pour le forward ?** Une commande est déjà une requête HTTP ; `ReverseProxy` la rejoue telle quelle avec le corps tamponné (≤ 32 KiB) et `SetXForwarded` ; l'instrumentation OTel est standard. gRPC n'apporterait rien à 2 pods. Je passerais à gRPC si les commandes devenaient un protocole interne distinct du protocole client.

**Pourquoi séparer gateway et gamesvc ?** Déployer la logique sans couper une seule WS ; répartir N spectateurs d'une partie chaude sur N gateways alors que l'écriture reste sur un gamesvc ; une seule connexion par client quel que soit le nombre de parties regardées.

**N'est-ce pas sur-architecturé ?** Oui pour le trafic réel, et je l'assume. Le monolithe LISTEN/NOTIFY était supérieur *en nombre de pièces* ; il avait un bug de correction (timers dupliqués) que seul le single-writer élimine structurellement. Chaque étape de la migration était déployable seule et enseignait un concept. Je sais dire lequel déployer selon le contexte.

**Comment garantis-tu l'ordre des événements côté client ?** `seq` monotone alloué sous le verrou de ligne ; le client maintient `lastSeq`, ignore `≤`, rattrape si `> lastSeq + 1`, et sérialise le traitement via une chaîne de promesses pour qu'un catch-up soit attendu avant l'événement suivant.

**Et si un pod traite une commande sans être propriétaire (repli local) ?** Il écrit en **époque 0** = non fencé, sérialisé par la base exactement comme avant l'affinité. C'est le chemin dégradé volontaire : le routage a le droit de se tromper, l'écriture non. Puis `Invalidate` sur le vrai propriétaire le fait réadopter et réarmer la pendule.

**Pourquoi le matchmaker a besoin d'un leader alors que `SKIP LOCKED` suffit ?** `SKIP LOCKED` garantit la sûreté (pas de double appariement) ; le leader évite que N pods tickent pour rien et rend le doorbell efficace. Le chevauchement bref de deux leaders lors d'un takeover reste sûr grâce à `SKIP LOCKED`.

**Limites connues de la version actuelle ?** Rate limit per-pod ; timer de grâce sur déconnexion local à la connexion ; `AppendMove` non fencé (seul `game_events` et `UpdateOutcome` le sont) ; `/readyz` sans check de dépendance ; Redis/Loki/Tempo/Prometheus mono-replica.

### Base de données

**Pourquoi Postgres pour les leases plutôt que Redis (`SET NX PX`) ?** Une seule horloge (`NOW()`), transactionnel, le fencing se fait dans la *même* instruction que l'écriture du journal — impossible avec deux systèmes. Redlock est critiqué précisément parce qu'il n'a pas de fencing.

**Comment fonctionne la séquence `seq` ?** `UPDATE games SET event_seq = event_seq + 1 … RETURNING` dans un CTE : le verrou de ligne sérialise, la valeur alimente l'`INSERT` dans la même instruction.

**Migrations ?** goose, SQL embarqué dans le binaire, appliquées au démarrage (`Open`). 20 migrations. Les intéressantes : `00007` (backfill de règles qui **épargne les parties finies** pour ne pas invalider le replay), `00009` (`DROP DEFAULT` volontaire sur `mode` pour qu'un futur mode ne tombe pas en 1v1 par accident), `00014` (postmortem UUID vs TEXT).

**Un exemple de race corrigée en SQL ?** L'acceptation de rematch : deux `UPDATE` aveugles sur un JSONB depuis deux pods ⇒ lost update. Corrigé par un `SELECT … FOR UPDATE` dans une transaction (`MergeRematchAcceptance`). Point important : le fix précédent (persister l'offre) couvrait un *autre* mode de panne du même bug.

### Kubernetes & infra

**Pourquoi k3s et pas EKS/GKE ?** Coût (~23 €/mois tout compris vs ~70 $/mois le seul control plane EKS) et apprentissage : je voulais *voir* etcd, le join, les certificats, le LB. En entreprise je prendrais un managé.

**HA : comment ça marche concrètement ?** 3 CP avec etcd embarqué (Raft, quorum 2/3), LB Hetzner devant `:6443` ; le premier CP fait `--cluster-init`, les autres `--server https://LB:6443` ; `serial: 1` dans Ansible pour ne pas courser le bootstrap etcd ; workers et kubeconfig pointent sur le LB. Les SPOF restants : le worker unique, Redis, Loki/Tempo/Prometheus mono-replica, une seule région.

**Zéro-downtime : quel est le mécanisme ?** `maxSurge 1 / maxUnavailable 0` + readiness probe + `preStop sleep 5` (drain des endpoints Traefik) + `Shutdown` 10 s côté Go + `leases.Close()` qui rend les parties immédiatement. Avec le split, les WS vivent sur les gateways : un déploiement de gamesvc ne coupe rien.

**Pourquoi ArgoCD (pull) plutôt que `kubectl apply` en CI (push) ?** Pas de kubeconfig hors du cluster, réconciliation continue (drift corrigé), audit dans Git, rollback = revert, l'état désiré est lisible sans accès au cluster.

**Comment la CI déclenche un déploiement sans toucher au cluster ?** Elle construit, pousse `:sha`, fait `kustomize edit set image` et **commit dans `main`** avec `[skip ci]` ; ArgoCD voit le commit et sync. Le seul token est `GITHUB_TOKEN` avec `contents: write`.

**Comment gères-tu les secrets ?** Voir §5.5. Phrase de synthèse : « le cluster s'authentifie auprès d'AWS avec l'identité de ses ServiceAccounts, jamais avec des clés. »

**Terraform : state et verrou ?** S3 chiffré + DynamoDB ; `plan` en PR commenté, `apply` gaté par un environment GitHub avec approbation ; on applique **le plan sauvegardé**, jamais un nouveau plan implicite.

**Terraform vs Ansible : pourquoi les deux ?** Terraform crée (VMs, réseau, LB, DNS, IAM) ; Ansible configure (k3s, add-ons) et peut être rejoué. L'inventaire dynamique fait le pont.

**Sécurité des pods ?** Distroless nonroot, UID 65532 explicite (kubelet ne peut pas vérifier un `USER` symbolique), rootfs lecture seule, capabilities droppées ; exception documentée pour Caddy (`cap_net_bind_service`).

**Ce qu'il manque ?** PodDisruptionBudgets, HPA, NetworkPolicies, receiver Alertmanager, SHA-pinning sur `deploy.yml`/`test.yml`, `publish-jwks` encore manuel, images single-arch, worker unique.

### Observabilité

**Comment as-tu évité l'explosion de cardinalité ?** `route` = `r.Pattern` du mux (`GET /api/games/{id}`), `unmatched` sinon ; `/ws/*` exclu ; labels métier bornés (`outcome`, `actor`, `op`).

**Une trace traverse deux pods : comment ?** Propagation W3C `traceparent` : `otelhttp.NewTransport` côté proxy injecte, `otelhttp.NewHandler` sur le listener interne extrait ; `otelsql` ajoute les spans SQL. Bug initial : les deux moitiés manquaient (deux traces déconnectées) — PR #45.

**Quelle alerte est la plus importante ?** `GemlinePersistErrors` : le Store *avale* volontairement les erreurs de persistance (la mémoire gagne, l'utilisateur ne voit rien), donc ce compteur est le seul signal qu'un redémarrage coûterait de l'état.

**RED ?** Rate, Errors, Duration — les trois premiers panneaux du dashboard.

### Go

**Comment gères-tu la concurrence dans le serveur ?** Un mutex par `GameRecord` (le moteur n'est pas thread-safe, et ne doit pas l'être) ; `clockManager` avec une `CancelFunc` par partie, cancel + install atomiques sous un mutex ; goroutine de réception Redis unique avec handlers non bloquants ; `-race` en CI.

**Arrêt gracieux ?** Signal → `Shutdown` avec contexte 10 s → `defer` LIFO : elector resign, bus close, store close (timers), `leases.Close()` (DELETE all), pool close, tracing flush 5 s. Ordre de démarrage contraint et commenté (tracing avant DB, `SetLeaseManager` avant `Start`, `server.New` avant `bus.Start`).

**Tests ?** `go test ./...` hermétique (miniredis, tests DB skippés sans `GEMLINE_TEST_DATABASE_URL`) ; suite intégration contre un vrai Postgres (replay-équivalence, leases, fencing, takeover, sweeper, forward, invalidation cross-pod) ; advisory lock session-scoped dans `TestMain` pour sérialiser les paquets ; gate 95 % sur le moteur (réel 100 %) ; `SMOKE_TESTS.md` pour les scénarios navigateur.

**Pourquoi 100 % sur le moteur seulement ?** C'est le paquet où un bug est *silencieux* (une partie corrompue sans crash) et le seul où 100 % est atteignable à coût raisonnable (fonctions pures, horloge injectée). Ailleurs, la valeur est dans les tests d'intégration, pas dans le pourcentage.

### Frontend

**Comment le client gère-t-il reconnexion et cohérence ?** Singleton WS par partie, backoff exponentiel avec jitter, `hello {token}` renvoyé à chaque reconnexion (annule le timer de grâce de 60 s), `lastSeq` + catch-up, dédup. Snapshot optimiste fusionné avec le live **seulement si strictement plus de coups** (le `>=` masquait les transitions qui ne bougent pas `moveCount` — bug vécu).

**Le JWT expire pendant que l'onglet dort ?** Le WS lobby échoue, le navigateur ne donne pas le code de fermeture ⇒ après le premier échec de reconnexion on rafraîchit le token Supabase avant de réessayer.

### Produit / posture

**Pourquoi ce projet ?** Un terrain où la *correction* est observable (une partie qui se corrompt se voit) et où le temps réel force les vrais problèmes distribués, tout en restant assez petit pour être construit et opéré seul de bout en bout.

**Qu'est-ce que tu referais différemment ?** Sealed Secrets → j'irais directement sur ESO + un backend cloud OIDC ; le backplane LISTEN/NOTIFY je le referais quand même (c'était la bonne première étape) mais je gaterais les timers sur une notion de propriété dès le début ; je mettrais la réponse `/readyz` sur un vrai check de dépendances ; je factoriserais les cinq `acquire*` du client WS.

**La plus grosse leçon ?** « Le transport est lossy, le journal est la vérité, le client rattrape par offset. » Poser cet invariant tôt a permis de changer *deux fois* de transport et de découper le backend sans toucher au protocole client.

---

## 8. Chiffres à retenir

| Quoi | Valeur |
|---|---|
| Plateau | côté 11, 331 intersections, tableau plat 21×21 = 441 (`-1` hors plateau), 3 axes, 50 gemmes/joueur |
| Victoire 2 joueurs | 6-ligne, ou 3×5, ou 8×4, ou 12 paires capturées |
| Pendule | 10 min par défaut, Fischer 0 s, tolérance skew `elapsed < 0 ⇒ 0` |
| Lease | TTL 15 s, heartbeat 5 s, release-all 5 s |
| Sweeper d'orphelines | 10 s, lot de 20 |
| Grâce déconnexion | 60 s |
| Matcher | tick 1,5 s, lot 100, bande Elo 100 + 10·s (cap 1000), quorum multi 3 |
| Elo | K = 32, départ 1200 |
| WS | ping 25 s, timeout pong 10 s, write 5 s, buffer 16 msgs/connexion |
| Reconnexion client | 500 ms → 15 s, ±30 %, 10 tentatives |
| Catch-up | max 1000 événements |
| Corps HTTP | 32 KiB |
| Rate limits | create 1/2 s (burst 5), chat 1/s (5), enqueue 1/2 s (3), search 2/s (10) |
| Forward | dial 2 s, header 10 s |
| Arrêt | Shutdown 10 s, preStop 5 s, grace 30 s |
| Pool DB | 20 max / 5 idle / 30 min |
| Cluster | 3 CP + 1 worker `cx23` (2 vCPU/4 GB) + `lb11`, ~23 €/mois, `fsn1` |
| Pods | gateway ×2, gamesvc ×2, web ×3, redis ×1 |
| Requests/limits backend | 50m/64Mi → 500m/256Mi |
| Rétention | Prometheus 15 j, Loki 30 j, Tempo 30 j, S3 lifecycle 60 j |
| CI | Go 1.25, gate 95 % (réel 100 %), ~3 min push → live |
| Versions | k3s stable (1.35), ArgoCD 3.4.1, cert-manager 1.16.1, ESO 0.11.0, kube-prometheus-stack 75.6.0, Loki 6.18.0, Tempo 1.24.4, Alloy 0.9.2, Terraform 1.14.5 |
| Images | serveur ~15 MB distroless, web ~25 MB Caddy |
| Repo | 239 commits, 11 mai → 16 août 2026, ~14,7k lignes Go, 20 migrations |

---

## 9. War stories (format situation → cause → fix → leçon)

1. **Double forfait entre replicas** (`1fb8687` et PR #44). Tout pod qui cachait une partie armait sa pendule ⇒ deux forfaits, coups de bot dupliqués. Cause : les timers dépendaient de « j'ai la partie en mémoire », pas de « je la possède ». Fix : leases + fencing + un seul gate `ownsGame()` ; sweeper pour la reprise. Leçon : *un bug de correction distribué se résout par un invariant (single-writer), pas par un mutex de plus.*
2. **Acceptation de rematch perdue** (`caddc3b`). Deux joueurs acceptent en même temps sur deux pods ⇒ deux `UPDATE` aveugles, le second écrase le premier. Fix : `SELECT … FOR UPDATE`. Leçon : *le fix précédent corrigeait un autre mode de panne du même bug ; toujours demander « quelle course reste-t-il ? »*
3. **409 intermittent sur l'acceptation de nulle** (`ee19e73`). `draw_offer_by` vivait en mémoire ; le pod B rechargeait depuis la base et ne voyait pas l'offre. « Réessayer marche » = signature d'un bug multi-pod. Fix : colonne + persistance sous le même verrou, avant la notification.
4. **Matchmaking mort en prod sans que personne ne le voie** (`00014`). `user_id TEXT` vs `profiles.user_id UUID` ⇒ `operator does not exist: uuid = text` à chaque tick, loggé et ignoré. Fix : `ALTER … TYPE UUID USING`. Leçon : *une boucle de fond qui log-and-continue a besoin d'un compteur d'erreurs et d'une alerte* — d'où `persist_errors_total`.
5. **Erreurs avalées rendues visibles** (`0ef9fcf`). Sept `_ = err` ⇒ un compteur Prometheus à labels bornés + une alerte. Leçon : *une politique « la mémoire gagne » est acceptable seulement si elle est observable.*
6. **Push → pull pour les credentials de siège** (`af3fb79` → `c287dda` → `82dcccb`). Un push lobby perdu laissait un joueur spectateur de son propre siège. Fix : `POST /seat/resolve` (le joueur *tire* son token par JWT) ; les événements lobby ne sont plus que des signaux de navigation ; repli `GET /matchmake/current`. Leçon : *le push est une optimisation, le pull est le contrat.*
7. **Deux traces au lieu d'une** (`d329079`, PR #45). Le proxy n'injectait pas `traceparent` et le listener interne ne l'extrayait pas. Fix : `otelhttp` des deux côtés ; test avec un vrai `TracerProvider` (le noop a un span context invalide que le propagateur refuse d'injecter).
8. **Clé maître Sealed Secrets perdue au rebuild HA** (#38 → `ef7aa9c`). Le cluster détruit emportait la seule clé capable de déchiffrer les secrets committés. Fix : sortir les secrets du cluster (AWS SM) et remplacer les clés par de l'identité (OIDC). Leçon : *un secret dont la clé de déchiffrement vit dans l'objet éphémère n'est pas un secret durable.*
9. **`NoCredentialProviders` sur Loki** (`aed33bf`). L'annotation IRSA était inerte sans le `pod-identity-webhook` (ESO ne l'avait pas révélé car il a son propre chemin JWT). Leçon : *sur EKS on ne voit pas ce composant parce qu'il est fourni ; sur k3s il faut le connaître.*
10. **Drift ArgoCD permanent sur les ExternalSecrets** (`b14c4fe`). Le webhook ESO injectait des défauts absents de Git. Fix : écrire les défauts explicitement. Leçon : *webhook mutant et moteur de diff déclaratif sont des ennemis naturels.*
11. **Caddy en CrashLoop `operation not permitted`** : `drop ALL` sur un binaire portant `cap_net_bind_service` ⇒ le noyau refuse l'exec. Fix : garder la capability, `allowPrivilegeEscalation: false` seulement.
12. **Test flaky 1/5** (`596915d`). Le statut passait à `finished` sous `rec.mu` mais le nettoyage de `seatRefs` se faisait ensuite sous `s.mu` ; le test observait l'état intermédiaire. Fix : attendre la bonne condition, pas un `sleep`, et **ne pas toucher au code de prod** (la séquence était correcte).
13. **Tap-to-confirm mobile qui « gigote »** (`33c11b1`). Hitbox de ~14 px + réinitialisation du preview sur tout changement de *référence* du tableau `cells` (déclenché par un ping de présence sans rapport). Fix : état remonté dans la page, reset sur `moveCount`, bannière de confirmation hors du wrapper de zoom.

---

## 10. Pièges à éviter en entretien

- Ne pas dire « haute disponibilité » sans nuance : le **control plane** est HA ; Redis, Loki, Tempo, Prometheus et le worker sont mono-instance. Dis-le avant qu'on te le fasse dire.
- « En production » = *live, public, monitoré*, pas « fort trafic ». Si on demande le trafic : quelques parties, l'intérêt est la correction et l'exploitation, pas la charge.
- « Scaling horizontal » : réel (2 gateways + 2 gamesvc, aucun état sticky), mais l'échelle testée est petite. Le design est *prêt* pour plus, il n'a pas été *prouvé* à plus.
- Ne pas vendre le fencing sans savoir répondre à « et `AppendMove` ? » (non fencé ; les écritures qui comptent — journal et résultat — le sont).
- Sur l'ancien CV : ne pas prétendre que LISTEN/NOTIFY était sans défaut ; les limites (§3.4) sont précisément ce qui montre ta compréhension.
- Si on te demande « pourquoi pas X » (Kafka, NATS, gRPC, EKS, Vault, Helm pour l'app) : réponds par le *signal* qui justifierait X, pas par un jugement sur X.
- Vocabulaire : dis « lease » et « fencing token », pas « lock » ; « réconciliation », pas « déploiement automatique » ; « issuer OIDC », pas « clé AWS ».

---

## 11. Check-list de dernière minute

- [ ] Savoir dessiner les deux schémas (§3.1 et §4.1) au tableau en 60 s chacun.
- [ ] Réciter le cycle de vie d'un coup dans les deux versions.
- [ ] Réciter l'upsert à trois cas du lease et l'append fencé (au moins en mots).
- [ ] Dérouler le flux OIDC/IRSA-equivalent en 6 étapes (§5.5).
- [ ] Dérouler la chaîne CI → GHCR → commit → ArgoCD → rollout.
- [ ] Trois war stories prêtes : timers dupliqués, rematch lost update, clé Sealed Secrets.
- [ ] Trois limites assumées : per-pod rate limit, mono-replica des briques d'infra, `publish-jwks` manuel.
- [ ] Relire `docs/architecture-notes.md` §9 (questions pièges) et le README « Architecture ».

---
---

# PARTIE II — Approfondissements de la visite guidée

> Compléments issus de la session de préparation du 1–2 septembre 2026. La partie I est le support de révision rapide ; celle-ci est la deuxième lecture : les fondamentaux reconstruits, les débats d'architecture, et les formulations forgées pendant la visite.

## 12. Consensus, quorum, HA — les fondamentaux

### Le tableau du quorum (à savoir refaire de tête)

Règle unique : **pour décider, il faut une majorité stricte des membres — vivants ou non**. Le total ne change pas quand un nœud tombe.

| Nœuds | Majorité | Pannes tolérées |
|---|---|---|
| 1 | 1 | 0 |
| 2 | 2 | **0** (pire que 1 : deux machines, même tolérance, double proba de blocage) |
| 3 | 2 | 1 |
| 4 | 3 | 1 (le 4e ne sert à rien) |
| 5 | 3 | 2 |
| 7 | 4 | 3 |

L'impair n'est pas requis « pour le consensus » : le pair est simplement **inutile** (n'ajoute aucune tolérance). D'où `cp_count ∈ {1,3,5,7}` validé dans Terraform.

### Pourquoi 2 CP en « leader + failover » ne marche pas

Un follower **ne peut pas distinguer** un leader mort d'un leader injoignable (partition, pause GC) — résultat fondamental des systèmes distribués. Avec la règle « plus de nouvelles → je me promeus » : une coupure réseau crée **deux leaders**, deux historiques divergents, infusionnables au retour du réseau (split-brain = corruption, pire qu'une indisponibilité). La majorité remplace « je ne le vois plus » par « **est-ce que je vois plus de la moitié ?** » — deux moitiés ne peuvent jamais répondre oui en même temps (deux majorités partagent toujours un membre). À 2 nœuds, partition 1|1 : personne n'a la majorité → tout s'arrête. Les systèmes qui veulent 2 replicas ajoutent toujours un **arbitre** : nœud témoin (Redis Sentinel, MongoDB arbiter), verrou externe, ou fencing matériel (STONITH).

**Le parallèle Gemline (à placer)** : les gamesvc font du failover à 2 pods sans split-brain *parce que Postgres est l'arbitre* (verrou de ligne + `NOW()` + fencing). etcd n'a personne en dessous — il *est* l'arbitre — donc il doit voter.

### Mécanique d'élection Raft (etcd)

- Un leader, des followers ; heartbeats ~100 ms ; élection timeout aléatoire (~1 s, randomisé pour éviter les candidatures simultanées).
- **Follower tombe** : rien — le leader continue, 2/3 reste une majorité.
- **Leader tombe** : timeout → un follower devient candidat, incrémente le **term** (mandat), demande les votes ; élu à la majorité s'il a un journal au moins aussi à jour. 1–2 s d'indisponibilité en écriture ; les clients k8s réessaient tout seuls.
- **Le term est un fencing token** : l'ancien leader qui revient voit un term supérieur et se rétrograde.
- Écriture : committée quand une **majorité l'a écrite sur disque** (leader + 1 follower à 3 nœuds) — d'où la sensibilité d'etcd à la latence disque.
- Partition 1|2 : le côté 2 continue, le nœud isolé **s'abstient de lui-même**.

### Trois niveaux d'élection dans le projet (symétrie à énoncer)

| Niveau | Mécanisme | Fencing |
|---|---|---|
| etcd | Raft (vote majoritaire) | `term` |
| Controllers k8s (controller-manager, scheduler) | Lease API Kubernetes | — |
| Matchmaker + parties Gemline | table Postgres (lease + heartbeat) | `epoch` |

> « Même idée à trois étages : heartbeat, TTL, mandat croissant. En bas on vote (pas d'arbitre disponible), en haut on délègue l'arbitrage à l'étage du dessous. »

### Nœuds CP schedulables (taints)

k3s ne pose **aucun taint** sur ses serveurs (contrairement à kubeadm/EKS : `node-role.kubernetes.io/control-plane:NoSchedule`). Donc les 3 CP sont aussi des workers : « 3 CP pour la HA du control plane, 4 nœuds de calcul ». Risque borné par les requests/limits (etcd est sensible à la latence disque). En entreprise : CP dédiés et taintés (`--node-taint CriticalAddonsOnly=true:NoExecute`). Vérifié en live : aucun taint, pods gemline répartis sur les 4 nœuds.

### Deux CP tombent : cluster « gelé », pas éteint

Plus de quorum → etcd refuse d'écrire → API en erreur, plus de scheduling ni de déploiement. **Mais les pods en cours continuent de servir.** Distinction à faire : indisponibilité du *pilotage*, pas du *service*.

## 13. TLS et certificats — les fondamentaux

### Signer ≠ chiffrer (la confusion classique)

Une paire de clés asymétriques a deux usages **différents** :
- **Chiffrement** : on chiffre avec la publique, seule la privée déchiffre (confidentialité).
- **Signature** : on signe avec la privée, n'importe qui vérifie avec la publique (authenticité).

Un certificat TLS = clé publique + nom (SAN) + signature d'une CA. Son rôle premier : **prouver l'identité** du serveur. Le trafic n'est PAS chiffré avec la clé du certificat : le handshake TLS 1.3 fait un échange **Diffie-Hellman éphémère** (le serveur *signe* cet échange avec sa clé privée), qui dérive une **clé symétrique de session** (AES-GCM) jetée en fin de connexion → **forward secrecy** (une clé privée compromise demain ne déchiffre pas le trafic d'hier).

De même, un **JWT n'est pas chiffré** : lisible par tous (base64), infalsifiable sans la clé privée. Les clés JWKS (bucket S3, Supabase) sont des clés de **vérification**, jamais de chiffrement. Les credentials STS voyagent simplement sous TLS ordinaire.

### Les deux histoires de certificats du projet (ne jamais les mélanger)

| | API Kubernetes (:6443) | Site public (:443) |
|---|---|---|
| CA | **interne k3s** (générée au cluster-init, connue de personne d'autre) | **Let's Encrypt** (publique, dans tous les navigateurs) |
| Clients | kubectl, kubelets, CPs qui rejoignent | les navigateurs du monde |
| Particularité | `--tls-san=<IP du LB>` obligatoire (on contacte l'API via le LB, l'IP doit être dans le certificat) ; mTLS (certificat client dans le kubeconfig) | émis/renouvelé par **cert-manager** (le « certbot Kubernetes »), challenge HTTP-01, stocké dans le Secret `gemline-tls`, présenté par Traefik |

Flux HTTP-01 : Ingress annoté → cert-manager demande à LE → LE exige un token sur `http://domaine/.well-known/acme-challenge/...` → pod+route temporaires via Traefik port 80 → vérif → certificat émis → renouvelé à J−30. Conséquences : port 80 ouvert, **Cloudflare `proxied=false`** (le proxy intercepterait le challenge ; alternative : DNS-01 par TXT), LB en **passthrough** (sinon le certificat sortirait du cluster). Rate limit LE : 5/semaine → staging pour les rebuilds.

Pont Java : erreur `PKIX path building failed` = le client ne connaît pas la CA (l'étape 1 de la vérification) — truststore.

## 14. Réseau et exposition

### NIC privée, réseau Hetzner routé

Chaque VM a 2 cartes : publique (`eth0`) et privée (`enp7s0`, réseau `10.0.0.0/16` créé par Terraform — etcd, kubelet↔API, LB→nœuds, flannel passent par là). Hetzner attribue l'IP privée par **DHCP, pas fiable au premier boot** sur Ubuntu 24.04 minimal → **fallback** : cloud-init attend jusqu'à 2 min puis configure à la main ; le rôle Ansible `networking` refait la vérif idempotente. Détail : réseau Hetzner **routé, pas switché** → IP en `/32` + routes explicites via la passerelle `10.0.0.1` (sinon `no route to host`).

### ClusterIP (et les autres types de Service)

Service = nom DNS stable + **IP virtuelle** + sélecteur de labels ; kube-proxy (iptables) redirige chaque **connexion** vers un pod ready, choisi au hasard **à l'établissement** (pas de mémoire ; keep-alive peut réutiliser le même pod, aucune garantie). ClusterIP = interne au cluster uniquement (frontière de sécurité gratuite). NodePort = port sur chaque nœud (à éviter) ; LoadBalancer = LB cloud public ; headless = DNS → IPs des pods.

Le LB Hetzner cible les CP par **label selector** (`role=control-plane`) : ajouter un CP ne demande aucun changement — même idée qu'un Service k8s qui sélectionne ses pods.

### Les « 4 portes » — auditer l'exposition Internet

```
Internet → ① Firewall Hetzner (22/80/443, 6443 sur IP) → ② services du LB (80/443/6443)
        → ③ Ingress Traefik (LA liste exhaustive de ce qui est publié) → ④ type du Service
```
Règle : **rien n'est exposé par défaut** ; ClusterIP + absent de l'Ingress = invisible d'Internet (gamesvc, Redis). Vérifier : `kubectl get ingress`, `kubectl get svc` (TYPE), curl externe, nmap du LB. Nuances : « interne » ≠ protégé des pods du cluster (→ NetworkPolicies, absentes, assumées) ; `/metrics` est servi par le binaire mais non routé par l'Ingress (404 Traefik dehors, scrape dedans).

### Routes publiques vs listener interne

Le listener interne `:8090` ne sert pas d'autres routes : il sert **la même API** (`s.apiHandler()`), enveloppée dans `markForwarded` (tag dans le **contexte**, pas un header → non spoofable puisque le listener est injoignable de dehors — « l'injoignabilité est ce qui rend le tag digne de confiance »), plus `/metrics` et les probes (seul listener du gamesvc). Public `:8080` : API + WS + probes + `/metrics` monté au-dessus de la chaîne de middlewares. Gateway ouvre le public seulement, gamesvc l'interne seulement, `ROLE=all` les deux.

## 15. IAM, OIDC, secrets — approfondissements

### Un Secret Kubernetes

Objet natif pour les valeurs sensibles, exposé en `envFrom` ou fichiers montés. **Base64 = encodage, pas chiffrement** ; la protection réelle = RBAC (+ chiffrement etcd au repos, non activé ici). C'est la **destination**, pas la source : Secrets Manager (durable) → ESO (synchroniseur, refresh 1 h) → Secret K8s → `envFrom`. Le pod ignore AWS ; remplacer le backend ne change pas le Deployment.

### Rôle IAM : trust policy vs permission policy

Un rôle = identité **sans clé**, qu'on *assume* (STS → credentials ~1 h). Deux policies à lire ensemble : **trust** (« *qui* peut l'assumer » — `Principal Federated` + conditions `sub`/`aud`) et **permission** (« *quoi* » — actions sur ARN nommés). Les 5 rôles :

| Rôle | Assumé par (`sub`) | Droits |
|---|---|---|
| `gemline-eso-secrets-reader` | SA `external-secrets:external-secrets` | GetSecretValue sur 2 secrets |
| `gemline-loki-storage` | SA `monitoring:loki` | RW son bucket |
| `gemline-tempo-storage` | SA `monitoring:tempo` | RW son bucket |
| `gemline-tf-plan` | GitHub Actions (PR) | lecture + la seule valeur `gemline/tf-vars` |
| `gemline-tf-apply` | GitHub Actions (main + approbation) | écriture scoped `gemline-*` |

Moindre privilège : un rôle par consommateur, ressources nommées (jamais `*`, sauf `ListSecrets` que l'API ne sait pas scoper), lecture/écriture séparées pour la CI.

### Les 4 attaques et pourquoi elles échouent (Q&A à réciter)

1. **Forger un JWT avec le bon `sub`** : pas la clé privée du cluster → signature invalide contre le JWKS du bucket.
2. **Monter SON cluster et signer** : son `iss` n'est pas enregistré dans le compte (le provider IAM est un acte d'admin explicite) ; s'il ment sur l'`iss`, STS vérifie contre **mes** clés → mismatch.
3. **Remplacer les clés du bucket** : lecture publique sur 2 clés seulement, **écriture derrière IAM** ; thumbprint TLS épinglé contre le MITM. (Publier des clés *publiques* n'est pas une fuite — c'est leur rôle.)
4. **Voler le jeton d'un autre pod** : mauvais `sub` → trust policy → refus.

> « Le jeton dit qui il est ; c'est AWS qui décide qui il croit. La chaîne de confiance remonte toujours à un acte d'administration explicite : l'enregistrement du provider. »

### Pourquoi l'issuer est un bucket S3 et pas l'URL du cluster

Un issuer OIDC = **deux documents JSON statiques** (`openid-configuration`, `jwks`), pas un service vivant. Servir depuis l'API : (a) son certificat est signé par la CA interne — AWS exige du TLS publiquement valide ; (b) ouvrir 6443 au monde pour 2 fichiers = toute l'API exposée (et STS n'a pas d'IP fixe à allowlister) ; (c) couple la disponibilité de l'issuer au cluster (il doit survivre aux rebuilds) ; (d) **EKS fait pareil** — son issuer est hébergé par AWS, pas par l'API server. `make publish-jwks` = photocopier les 2 fichiers (`kubectl get --raw` + `aws s3 cp`, garde-fou `test -s` contre l'upload vide).

**Œuf-et-poule de l'automatisation** : un CronJob in-cluster aurait besoin de credentials AWS… obtenus par l'OIDC… cassé tant que le JWKS n'est pas publié. Le bootstrap de confiance vient forcément de l'extérieur → l'automatisation correcte = dernière tâche du playbook Ansible (mes credentials). Point « ce que j'améliorerais ».

### IRSA — définition et les 4 pièces qu'EKS masque

IRSA = *IAM Roles for Service Accounts* : un rôle IAM **par ServiceAccount** (vs avant : clés en Secret, ou rôle du nœud partagé par tous les pods). Sur EKS, AWS fournit : l'issuer hébergé, les flags API server, (presque) le provider IAM, et le **pod-identity-webhook** préinstallé. Self-hosted, tout est reconstruit : bucket S3 + publish-jwks, flags via Ansible, provider via Terraform, webhook via ArgoCD.

**Pod-identity-webhook** : mutating admission webhook qui, à la création d'un pod dont le SA porte `eks.amazonaws.com/role-arn`, injecte `AWS_ROLE_ARN`, `AWS_WEB_IDENTITY_TOKEN_FILE` et le token projeté (`aud=sts.amazonaws.com`). Le SDK AWS (tout langage) consomme ça **nativement** — zéro ligne de code. L'annotation seule est **inerte** : ESO marchait sans (chemin `auth.jwt` propre), **Loki plantait** (`NoCredentialProviders`) → installation du webhook. Deux façons de consommer l'OIDC : intégrée (ESO) ou transparente (SDK + webhook). Depuis fin 2023, EKS propose aussi *EKS Pod Identity* (agent sur nœud, plus simple) — propre à EKS, pas transposable self-hosted.

### `--cluster-init` (où et quoi)

Flag k3s : « fonde le cluster **etcd embarqué** » (sans lui : SQLite, pas de HA). Un seul nœud le passe (cp1, `is_first_cp` = premier du groupe d'inventaire) ; il crée l'etcd à 1 membre, **la CA et les clés de signature des SA** (d'où re-publish-jwks après rebuild). Les autres : `--server=https://LB:6443` (jamais un CP précis — survivre à sa perte) + token k3s (Vault). `serial: 1` : le join ne doit jamais précéder la fin du bootstrap (sinon : deux clusters fondés, ou join raté). Où le voir : `roles/k3s_server/tasks/main.yaml:47` ; sur la machine : `ExecStart` dans `/etc/systemd/system/k3s.service` ; effet : ROLES `control-plane,etcd` dans `kubectl get nodes`. Historique : le cluster mono-CP initial était en SQLite → migration en place délicate → rebuild (et perte de la clé Sealed Secrets).

## 16. Concurrence dans le backend

### Mutex : quoi, où

Mutex = exclusion mutuelle (`Lock`/`Unlock`, une goroutine à la fois) ; RWMutex = N lecteurs OU 1 écrivain ; `-race` en CI. **9 mutex dans le code, petits et spécialisés, un par ressource** : `GameRecord.mu` (l'état d'UNE partie — verrou par partie, deux parties en parallèle), `Store.mu` (la map du cache), `clockManager.mu` (timers pendule, cancel+install atomique), `presenceManager.mu`, `Hub.mu` (RW — abonnés WS), `ratelimit.mu`, `bus.mu` (refcounts), `lease.Manager.mu`, `elector.mu`.

### La hiérarchie de sérialisation (le moteur n'est pas thread-safe : c'est voulu)

```
moteur  : GameState — zéro verrou, zéro I/O   → pur, testable à 100 %
pod     : GameRecord.mu — LE verrou, par partie
cluster : lease — une partie = un seul pod
```
La thread-safety n'est pas une propriété du moteur, c'est une propriété de l'architecture (modèle acteur). Mettre des verrous dans le moteur = payer la complexité là où, par construction, un seul fil passe.

Pont Java : `sync.Mutex` ≈ `ReentrantLock`/`synchronized` (mais **non réentrant** : re-lock même goroutine = deadlock) ; `RWMutex` ≈ `ReadWriteLock` ; `-race` : pas d'équivalent direct.

## 17. Le cycle du coup — précisions issues des questions

### Le « pool » `gemline-gamesvc:8090`

ClusterIP ordinaire, choix **par connexion, au hasard, sans mémoire**. Ce n'est **pas** le chemin nominal : partie avec propriétaire → le gateway dial **directement l'IP du pod** (`owner_addr` de la table de leases), Service contourné. Le pool ne sert que : partie **sans** propriétaire (le pod tiré au sort la revendique → le hasard du kube-proxy EST le répartiteur d'attribution initiale) et `POST /api/games` (pas encore d'ID). Pas headless : on veut l'IP virtuelle qui choisit ; le « choix précis » passe par la table de leases, pas le DNS.

**Capacité** : aucune limite explicite de parties par gamesvc — bornes implicites : mémoire (GameRecord ~dizaines de Ko, limite 256 Mi → milliers de parties), CPU (bots), pool DB (20). **Pas de rééquilibrage** : une partie reste chez son propriétaire jusqu'à fin/crash/deploy ; si besoin un jour, `Release` = handover immédiat + sweeper — le mécanisme existe déjà. « Problème que je documente, pas que je résous. »

### `moves` vs `game_events` (pourquoi deux journaux)

| | `moves` | `game_events` |
|---|---|---|
| Ligne | le **fait** brut typé (ordinal, couleur, q, r, played_at) | le **message client** (seq, type, payload JSONB = le DTO du WS) |
| Sert | reconstruire l'état par replay moteur | catch-up `?since=` — mêmes objets que le live |
| Lecteur | le serveur | le navigateur |

On ne garde pas que `moves` : le flux contient des non-coups (chat, state, rated) que le catch-up doit servir, déjà mis en forme. On ne garde pas que `game_events` : payloads = **instantanés** (rejouer des photos ≠ rejouer les règles — un bugfix du moteur doit se refléter au replay), typage couplé au protocole, cycles de vie différents (`moves` permanent, `game_events` purgeable). > « Journal du domaine vs journal du transport. » Si « c'est du CQRS ? » : cousin, mais `game_events` n'est pas une projection d'état, juste le flux sortant persisté. `seq` ≈ **offset Kafka** (log ordonné par partie, consommateurs qui se resynchronisent par offset) ; ≠ `ordinal` (coups seulement).

### Le test `podId == moi` — trois cas réels

Tout **écrivain est aussi abonné** à ses propres canaux (l'abonnement suit le **cache**, pas le rôle) :
1. **gamesvc** : abonné aux parties qu'il possède (cache du Store) → sans le test, il **s'auto-invaliderait après chaque coup** (jetterait la vérité live).
2. **gateway en chemin dégradé** : owner+pool injoignables → il traite localement, écrit epoch 0, **publie** → son propre message lui revient.
3. **ROLE=all** : publieur et serveur de WS dans le même processus (le cas historique).
Le handler ne connaît pas les rôles — « événement d'un autre → invalide ; de moi → ignore ; des spectateurs ? → livre » — le comportement correct **émerge** du rôle.

### Pourquoi le gamesvc écoute Redis pour SES parties (les deux détecteurs)

| Détecteur | Observe | Échelle |
|---|---|---|
| Lease | propriétaire ↔ **Postgres** (heartbeat 5 s, TTL 15 s) | ~15 s : « mort » |
| Forward | gateway → propriétaire **HTTP** (dial 2 s) | ~2 s : « injoignable là, maintenant » |

Ils peuvent être en **désaccord** : un gamesvc vivant (heartbeat OK) mais injoignable 3 s depuis un gateway (hoquet réseau, rollout). Timeline : forward échoue → pool → B écrit **epoch 0** (Acquire échoue, le lease de A est vivant !) → A n'a **jamais perdu son lease**, `onLost` n'a jamais tiré, sa mémoire a un coup de retard, sa pendule compte le mauvais joueur. **Redis est le seul messager** (B ne sait pas joindre A — sinon le forward aurait marché) : A reçoit l'enveloppe, `podId ≠ moi` → invalide, se réadopte, réarme. > « Chaque assouplissement d'un invariant (le single-writer cède devant la disponibilité) crée une obligation de réconciliation ailleurs. »

### L'epoch n'est PAS un temps

Compteur de **règnes** : +1 au changement de main uniquement (renouvellement = +0). Le temps vit dans `expires_at` (vivacité, comparé à `NOW()` Postgres uniquement) ; l'epoch départage les écrivains (un compteur sous verrou de ligne est strictement croissant et insensible aux horloges — un timestamp ne l'est pas). Écriture : pas de « check puis write » (race) — write **avec** l'epoch, la base tranche dans la même instruction. Fencé : `game_events` + `UpdateOutcome` (là où vivent les doubles forfaits) ; **pas `AppendMove`** (limite assumée : le zombie serait attrapé à l'`AppendEvent` suivant ; durcissement possible à citer soi-même).

## 18. Les trois débats d'architecture (les réponses forgées)

### « L'epoch 0, c'est bancal » — le cadrage CP/AP

Correction du modèle etcd d'abord : un leader isolé ne « perd » pas le lead — il ne peut plus **committer** (pas de majorité) et se rétrograde à son retour (term supérieur). Pendant la fenêtre, **etcd choisit l'indisponibilité** (CP) : en cas de doute, refuser de servir. Gemline choisit l'inverse sur son chemin dégradé (AP-flavored) et peut se le permettre pour une raison précise :

> **« etcd n'a personne en dessous de lui. Gemline a Postgres. »**

La mémoire du gamesvc n'est pas la vérité — un cache chaud avec privilèges. Toute écriture, fencée ou epoch 0, passe par le même verrou de ligne et le même `seq` : **le vrai single-writer de Gemline est le verrou de ligne Postgres** ; le single-writer applicatif est une couche d'optimisation qu'on a le droit de contourner. L'epoch 0 retombe sur l'architecture pré-affinité (2 mois de prod correcte) : dégradation de *performance*, jamais de *correction*.

Le choix inverse (strict, façon etcd : échouer le coup) est défendable — plus simple à raisonner, zéro réconciliation. Son prix : erreurs visibles à chaque hoquet de 2-3 s, TTL douloureux (15 s d'indisponibilité par mort de propriétaire, ou faux transferts si raccourci), et la détection « lent vs mort » ne disparaît pas. Même etcd, choix CP, a besoin du fencing (le term) : aucun choix n'élimine le zombie, on choisit ce qui se passe *pendant* qu'il existe.

> Concession-cadrage : « C'est le coût d'un choix : disponibilité d'abord. Si l'exigence s'inversait, je sais exactement quoi changer : supprimer le repli local, retourner 503, raccourcir le TTL. Le design est un curseur, pas un dogme. » Et : « un consensus par partie coûterait 3 replicas d'état et des round-trips majoritaires par coup, pour un problème que le verrou de ligne d'une base déjà présente résout gratuitement — on ne paie Raft que sans arbitre en dessous. »

### « Pourquoi ne pas mettre le payload complet dans Redis ? » (idée légitime — Lichess le fait)

Gain : un SELECT par clé primaire (sub-ms), déjà **évité sans spectateurs**. Ce que les IDs seuls achètent : (a) l'enveloppe sert **aussi** d'invalidation de cache (payload inutile pour ça) ; (b) coût du bus borné — pub/sub **copie vers chaque abonné**, un event `state` = 441 cases ; (c) **une seule autorité de format** : live et catch-up relisent la même ligne → divergence impossible ; (d) le contrat « le bus n'est pas une vérité » devient inviolable par construction ; (e) historique : migration LISTEN/NOTIFY→Redis = routage seulement, payload inchangé.

> « Si le profiling montrait que cette lecture pèse, j'ajouterais le payload dans l'enveloppe comme pur cache d'optimisation — le `seq` et le rattrapage journal restent le filet, le contrat ne change pas. »

### « Le client voit le trou quand ? » — silence ≠ trou

Un trou n'est détectable **qu'à l'arrivée de l'événement suivant**. Pendant une panne Redis : les clients distants voient du **silence**, indistinguable de « rien ne se passe » (les pings WS passent — ils prouvent le *tuyau*, pas la *complétude du flux*). Resynchronisation déclenchée par : (1) premier événement publié après le retour de Redis (seq sauté → catch-up, qui récupère TOUT le manqué), (2) toute **reconnexion WS** (le serveur envoie un `state` complet à la connexion), (3) une action du client (la réponse HTTP révèle l'état frais). Détail UX : la pendule affichée continue de tourner localement — l'écran a l'air vivant, le plateau est gelé. Le correctif classique, absent et assumé : **heartbeat applicatif portant le `seq` courant** (transforme le silence en information vérifiable) ou resync par inactivité. > « Liveness de la connexion ≠ complétude du flux. »

## 19. Modes dégradés — l'inventaire systématique

Principe : toutes les pannes convergent vers **un seul contrat de récupération** (journal = vérité, `seq`, replay, leases).

| Panne | Survie | Perte |
|---|---|---|
| Propriétaire injoignable 2-3 s | repli owner→pool→local, epoch 0 sérialisé, réconciliation Redis + réadoption | rien (un rechargement) |
| Propriétaire mort | lease expire ≤15 s ; reprise par trafic ou sweeper (10 s) ; replay + pendules depuis `played_at` | 10-25 s de pendule figée |
| Zombie | `ErrStaleLease` à la 1re écriture → jette son cache, se tait | rien |
| Redis down | publication logguée + livraison locale ; go-redis se réabonne ; catch-up par seq ; matcher : ticker 1,5 s | fraîcheur inter-pods pendant la panne |
| Pod Redis mort | replanifié, zéro persistance à restaurer | fenêtre de silence |
| Postgres down | **la falaise** : parties non cachées ne chargent plus, écritures en erreur ; la SPA s'affiche | le service de jeu |
| Écriture DB ponctuelle échoue | « la mémoire gagne » + `persist_errors_total` + alerte | risque à la prochaine éviction/restart |
| Supabase JWKS down (runtime) | JWT en cache OK, login cassé ; au **boot** : fail-closed (refuse de démarrer auth-désactivée) | logins |
| Sans `DATABASE_URL` / `AUTH_MODE` (config) | mémoire pure mono-processus / anonyme seulement | persistance / comptes |
| WS coupée | backoff 500 ms→15 s ±30 %, re-`hello` (annule le forfait), state + catch-up | rien |
| Client lent (buffer 16 plein) | drop pour lui seul, hub jamais bloqué, resync au trou suivant | rien |
| Push lobby perdu | push = signal de navigation ; pull durable (`/matchmake/current`, `/seat/resolve`) | rien |
| JWT expiré (onglet endormi) | refresh Supabase après le 1er échec de reconnexion | rien |
| Nœud down | LB éjecte ≤45 s ; topology spread absorbe ; reschedule ~5 min ; leases cf. supra | rien de durable |
| 1 CP down / leader etcd down | quorum 2/3 / réélection ~1 s | rien |
| 2 CP down | cluster **gelé** (plus d'API/scheduling) mais les pods servent | pilotage |
| Rollout gamesvc | WS sur les gateways ; `leases.Close()` = handover immédiat ; surge 1/unavailable 0 | rien |
| Leader matchmaker mort | électeur reprend ≤15 s (epoch++) ; file durable ; chevauchement neutralisé par SKIP LOCKED | latence d'appariement |
| Timer de grâce sur pod mort | le forfait « s'oublie », la partie attend l'adoption | un forfait (limitation documentée) |

**Ne dégradent PAS gracieusement** (à annoncer soi-même) : Postgres/Supabase (dépendance dure), une région, Redis/Loki/Tempo/Prometheus mono-replica, timer de grâce lié au pod.

> Synthèse : « Trois étages de dégradation, jamais de falaise : le routage peut se tromper (replis, epoch 0 sérialisé), le transport peut perdre (lossy par contrat, catch-up par offset), la propriété peut mourir (leases + sweeper + replay, zombies neutralisés par le fencing). La seule vraie falaise, c'est Postgres — et l'alerte sur les erreurs avalées existe précisément parce qu'une politique "la mémoire gagne" silencieuse serait dangereuse. »

## 20. ArgoCD / GitOps — approfondissements

### Push vs pull (l'argumentaire)

Push (`kubectl apply` en CI) : kubeconfig admin dans GitHub (le secret longue durée qu'on chasse partout), application seulement au moment du push (drift invisible entre deux), état réel écrit nulle part. Pull (GitOps) : agent **dans** le cluster, Git = source de vérité, **réconciliation continue** — le 3e réconciliateur empilé (Kubernetes, Terraform, ArgoCD : même verbe).

### La syncPolicy et ses conséquences

`automated` (sync sans clic) + `prune` (supprimé de Git ⇒ supprimé du cluster) + `selfHeal` (kubectl edit sauvage ⇒ réécrasé en secondes). Conséquence culturelle : le `kubectl apply` manuel est inutile ET impossible — toute modification durable est un commit. **Piège** : `kubectl rollout undo` créerait du drift que selfHeal écraserait — le rollback est un **commit** (revert, ou re-pin d'un SHA déjà construit via `kustomize edit set image`).

### L'ordre de l'app-of-apps est load-bearing

external-secrets (CRDs ESO) → eso-config (les CRs) → **pod-identity-webhook** (doit muter avant que les pods AWS naissent) → monitoring (CRDs ServiceMonitor/Rule) → loki/tempo/alloy (dépendent du webhook) → dashboards → gemline. Règle : CRDs avant CRs, webhooks d'admission avant leurs consommateurs. Deux types de sources : Helm pour l'upstream (values inline), git+Kustomize pour le sien (rien à templater). Les Applications sont appliquées depuis le **GitHub raw**, pas le checkout local : le cluster reçoit ce qui est committé.

### Les 4 frictions vécues (war stories de la couche)

1. **Webhook mutant vs moteur de diff** (`b14c4fe`) : ESO injecte des défauts absents de Git → OutOfSync permanent → écrire les défauts explicitement. « Un mutating webhook et un réconciliateur déclaratif sont des ennemis naturels. »
2. **ServerSideApply partout** : CRDs > 256 Ko de l'annotation client-side ; SSA = merge côté API server + propriété par champ (règle aussi les conflits Ansible-bootstrap / ArgoCD-run).
3. **ArgoCD v3** : v2 ignorait `.status.terminatingReplicas` sur k3s ≥1.31 → diff en échec, statut oscillant. « OutOfSync peut être un bug du comparateur. »
4. **`disableNameSuffixHash`** sur les ConfigMaps de dashboards : sinon hash de contenu → prune/recreate à chaque édition → dashboard qui clignote dans Grafana.

États à connaître : Synced/Healthy, OutOfSync (transitoire avec auto-sync), Degraded (appliqué mais malade), Unknown (repo illisible — cas type : passé en privé sans credentials).

## 21. Observabilité — approfondissements

### Le pipeline

```
pods ── /metrics (pull 30 s, 2 ServiceMonitors) ──▶ Prometheus ─▶ alertes/dashboards
     ── stdout slog JSON ─▶ Alloy (DaemonSet, via l'API pods, sans hostPath) ─▶ Loki ─▶ S3 30 j
     ── OTLP :4318 ──────▶ Alloy ─▶ Tempo ─▶ S3 30 j          (lifecycle S3 60 j = filet ×2)
```
Tout par ArgoCD, tout en code (dashboards/datasources en ConfigMaps ramassés par le sidecar, alertes en PrometheusRule). Piège kube-prometheus-stack : `*SelectorNilUsesHelmValues: false` sinon les ServiceMonitors hors chart sont ignorés.

### Discipline de cardinalité (le sujet où être incollable)

Une série par combinaison de labels. Trois décisions : `route` = **pattern du mux** (`GET /api/games/{id}`, `"unmatched"` sinon) ; `/ws/*` **exclu** du middleware (durée d'un handler upgradé = durée de la connexion — la gauge `websocket_connections` couvre le live) ; **jamais d'ID** en label (`op` = chaînes choisies à la main). Même logique côté **Loki** (n'indexe que les labels : 5 + `level` promu, le reste en `| json`) et côté **Tempo** (`patternSpanNamer` renomme les spans après dispatch du mux — sinon chaque URL concrète est un nom de span distinct).

### Les idiomes PromQL des alertes

- `or vector(0)` : pas de 5xx = zéro, pas « série absente ».
- `clamp_min(sum(rate(...)), 1e-9)` : jamais de division par zéro à trafic nul.
- `histogram_quantile(0.99, sum by (le) (rate(..._bucket[5m])))` : les histogrammes sont des compteurs par bucket (`le` = less-or-equal), le quantile est reconstruit à la requête.
- `for: 5m/10m` : anti-flap.
L'alerte qui compte : `GemlinePersistErrors > 0` — née du matchmaking mort en silence (uuid=text, tick loggé-ignoré). > « Une dégradation silencieuse n'est acceptable que si elle est observable. » Limite assumée : Alertmanager sans receiver.

### Le scénario de debug à raconter

> « Alerte p99. Dashboard : latence en hausse sur `POST /moves`, 5xx plat — lent, pas cassé. Tempo : traces > 500 ms sur cette route ; spans gateway et owner fins, span SQL `AppendEvent` à 400 ms — c'est la base. Clic tracesToLogsV2 (span→logs Loki ±1 min) : retries de pool au même instant. Direction Supabase : pooler saturé. Sans les traces j'aurais profilé mon Go pour rien ; sans le renommage des spans, impossible d'agréger par route. »

### Le pont Java

> « Micrometer + Actuator exposent `/actuator/prometheus` (le ServiceMonitor pointe dessus, rien d'autre ne change) ; Logback JSON pour Loki ; et le **javaagent OpenTelemetry** instrumente Spring MVC, JDBC et les clients HTTP sans une ligne de code — là où en Go j'ai enveloppé explicitement handler, transport et driver. Dashboards, alertes et discipline de cardinalité : identiques. »

## 22. Formules-clés forgées pendant la visite (le best-of à réciter)

1. « Le quorum est la majorité stricte ; le pair coûte un nœud de plus sans tolérer une panne de plus ; et la majorité, plutôt qu'un survivant, c'est ce qui empêche deux moitiés d'être leaders en même temps. »
2. « Un follower ne distingue pas un leader mort d'un leader injoignable — c'est pour ça qu'on vote, ou qu'on prend un arbitre. Mes gamesvc failover à 2 parce que Postgres arbitre ; etcd est l'arbitre, il n'a personne. »
3. « Le certificat prouve l'identité ; la clé privée signe la poignée de main ; le trafic est chiffré par une clé symétrique de session éphémère — forward secrecy. »
4. « Le jeton dit qui il est, AWS décide qui il croit : la chaîne de confiance remonte toujours à l'enregistrement explicite du provider. »
5. « Un issuer OIDC, c'est deux documents statiques — aucune raison de les servir depuis le control plane. EKS fait pareil. »
6. « La thread-safety n'est pas une propriété du moteur, c'est une propriété de l'architecture : zéro verrou dedans, un mutex par partie au-dessus, un lease par partie entre les pods. »
7. « Le vrai single-writer, c'est le verrou de ligne Postgres ; le gamesvc est une couche d'optimisation — c'est pour ça qu'epoch 0 est un repli sûr. »
8. « L'epoch est un compteur de règnes, pas un temps : +1 au changement de main, jamais au renouvellement. Un zombie peut croire, pas agir. »
9. « L'abonnement suit le cache, pas le rôle : quiconque tient une copie écoute son canal. Chaque assouplissement d'un invariant crée une obligation de réconciliation ailleurs. »
10. « Le lease détecte la mort en 15 s ; le forward détecte l'injoignabilité en 2 ; le chemin dégradé vit dans le désaccord entre les deux. »
11. « Liveness de la connexion ≠ complétude du flux : le ping prouve le tuyau, seul le seq prouve qu'on n'a rien raté. »
12. « Le routage peut se tromper, le transport peut perdre, les clients peuvent ramer — rien de tout ça ne corrompt, parce que la vérité est unique, ordonnée et verrouillée. Le reste est de l'optimisation de latence. »
13. « La CI ne parle jamais au cluster : elle écrit dans Git, le cluster vient se servir. Un déploiement est un commit comme un autre. »
14. « Avec GitOps, un webhook mutant et un moteur de diff sont des ennemis naturels : rends les défauts explicites. »
15. « Une politique de dégradation silencieuse n'est acceptable que si elle est observable — chaque erreur avalée incrémente un compteur, et ce compteur a une alerte. »
