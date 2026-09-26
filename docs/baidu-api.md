# Baidu Netdisk HTTP protocol spec (for the clean-room rewrite of `bnd`)

Status: research notes, 2026-09-26, from real HTTP recordings.

Tags used below:
- **[rec]** = observed in a recording (ground truth).
- **(unverified — from old code)** = only in old code; no recording exists. It may be stale.
- **[dead?]** = probably broken or unreachable today (see §14).

> **Recording caveat.** The cassette recorder *normalises* URLs: recorded query strings are sorted alphabetically, and
> form bodies are re-encoded. So the order you see in a recorded URL is **not** the wire order. This matters for
> `locatedownload`, where the old code puts the signature params last on purpose (§5.1). Values marked `REDACTED` are
> credentials (Cookie, BDUSS, bdstoken in requests).

---

## 0. Conventions shared by all endpoints

### 0.1 Hosts and API families

| Family | Base | Error envelope | Default UA |
|---|---|---|---|
| **PCS** | `https://pcs.baidu.com/rest/2.0/pcs/<sub>?app_id=<APPID>&method=<m>` (`sub` = `file`, `quota`, `superfile2`, `stream`) | `{"error_code":N,"error_msg":"..."}`; success = no `error_code`, or 0 | **none** (see 0.3) |
| **Pan** | `https://pan.baidu.com/api/<sub>` | `{"errno":N, ...}`; success = `errno` 0 | netdisk UA |
| **Pan share** | `https://pan.baidu.com/share/<sub>` (always https) | `errno` (+ `show_msg`) | netdisk UA or a browser UA (per endpoint) |
| **Pan rest** | `https://pan.baidu.com/rest/2.0/services/cloud_dl?method=...`, `.../rest/2.0/xpan/file?method=create` | PCS-style `error_code` (old code) | none |
| **Tieba** | `http://tieba.baidu.com`, `http://c.tieba.baidu.com` (plain http) | `error_code` (a **string** `"0"` in login, a number in profile) | own UA |
| **CDN** | `https://<node>.baidupcs.com/file/<enc-md5>?...` (download links) | HTTP status; errors may carry a PCS JSON body | netdisk UA (locate mode) |

- The scheme is `https` for everything except tieba. It is configurable (`enable_https`, default true), and the old
  code then switches PCS, Pan and download URLs to http. `share/*` is always https.
- **APPID.** PCS calls use the configured `appid`, default **`266719`** [rec]. Some calls hard-code **`250528`** (the
  official netdisk client app id): `locateupload`, `locatedownload`, `share/list`, `share/transfer`, `cloud_dl`, and
  the no-rapid precreate. The file's `app_id` in `meta` shows which app created it: files made with precreate/create
  show 250528, and a PCS `copy` made with 266719 shows 266719 [rec].
- `request_id` shows up in nearly every response. Ignore it.

### 0.2 Cookies / auth
- Cookies are held in a jar under domain `.baidu.com`, set against `http://pan.baidu.com`, and sent to
  `pan.baidu.com`, `pcs.baidu.com`, `*.pcs.baidu.com` and tieba.
- Minimum cookie: **`BDUSS`**. **`STOKEN`** (the *pan* STOKEN) is required for the share-transfer flow, because the
  share page only includes `loginstate`/`bdstoken` when it is present. The old code also recommends STOKEN for
  `user/getinfo` (it asks for STOKEN when that fails). Optional: `SBOXTKN`, `BAIDUID`, `PTOKEN` (stored but never sent).
- Old behaviour: when the user gave a full cookie string that contains `STOKEN=` and no separate `-stoken`, the whole
  cookie string goes into the jar. Otherwise the jar is `BDUSS` + `STOKEN` (written even when empty) + optional `SBOXTKN`.
- Servers answer with `Set-Cookie` on most pan/pcs calls (redacted in recordings). Keep a live jar: the share flow needs
  the `BDCLND` cookie set by `/share/verify`.
- **CDN download requests** (locate mode) carry the same cookies. The old code copies the jar onto the CDN host [rec:
  Cookie header present on `*.baidupcs.com` GETs]. After a PCS 302, the CDN request carries **no** cookie (the
  redirect crosses domains) [rec].

### 0.3 User-Agent strings (exact)
- `NETDISK_UA` = `netdisk;P2SP;3.0.0.8;netdisk;11.12.3;ANG-AN00;android-android;10.0;JSbridge4.4.0;jointBridge;1.1.0;`
  (config `pan_ua`). Used for all `pan.baidu.com/api/*`, `share/pset|cancel|record`, the share page `/s/<surl>`,
  `locatedownload` and CDN range GETs.
- `PCS_UA` = **empty** (config `pcs_ua`, default `""`). The recorded header is `User-Agent: [""]`. With Go's net/http,
  an explicitly empty UA means the header is **omitted entirely**; it is not replaced by `Go-http-client/1.1`. The new
  implementation must reproduce that: suppress the default UA. It is used for PCS `file/*`, `quota`, `superfile2`,
  `locateupload`, `/api/user/getinfo` (sic, the old code does not use the pan UA here) [rec], and pcs-mode download.
- `BROWSER_UA` = `Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_2) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/63.0.3239.132 Safari/537.36`
  (config `user_agent`). Used for `share/verify`, `share/list` and the tieba profile [rec].
- `TRANSFER_UA` = `Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/76.0.3809.100 Safari/537.36`
  (hard-coded, `share/transfer` only) [rec].
- `TIEBA_UA` = `bdtb for Android 6.9.2.1` (tieba login) [rec].
- `Mozilla/5.0` (the bare string): `GET /disk/home` for the web signature [dead?].

### 0.4 Body encodings
- **form**: `application/x-www-form-urlencoded`, with keys sorted by the encoder. JSON arrays are passed as form
  values, e.g. `path_list=["/a"]`, `fidlist=[1,2]`.
- **PCS multipart `param`**: `multipart/form-data; boundary=<60 hex chars>` holding a single part:
  ```
  --B\r\nContent-Disposition: form-data; name="param"\r\n\r\n{"list":[...]}\r\n--B--\r\n
  ```
  There is no Content-Type on the part [rec]. Used by meta, delete, copy, move and restore.
- **superfile2 multipart**: a single part `Content-Disposition: form-data; name="uploadedfile"; filename=""`, with
  raw bytes and no part Content-Type [rec].
- Some POSTs have **no body**: every parameter is in the query string (PCS `mkdir`, `cloud_dl`, `locatedownload`).

### 0.5 Error detection algorithm (old code, recommended)
```
body := read all
env := json(body)            # tolerate leading whitespace (share/verify body starts with "\n\n")
code := (family == Pan) ? env.errno : env.error_code
if code != 0 -> RemoteError{code, env.error_msg or table[code]}
```
The HTTP status is **not** reliable on its own: PCS returns 404 for 31066 and 400 for 31061, while pan returns 200
with `errno != 0`. Always parse the body. The upload path (`superfile2`) additionally treats 4xx/5xx as errors even
without a code.

---

## 1. Login and identity

### 1.1 Tieba login: validate BDUSS, get uid [rec]
`POST http://tieba.baidu.com/c/s/login`
- Headers: `Content-Type: application/x-www-form-urlencoded`, `Cookie: ka=open` (old code sets it explicitly),
  `net: 1`, `User-Agent: bdtb for Android 6.9.2.1`, `client_logid: <unix>416`, `Connection: Keep-Alive`.
- Form (before signing):
  `bdusstoken=<BDUSS>|null`, `channel_id=`, `channel_uid=`, `stErrorNums=0`, `subapp_type=mini`, `timestamp=<unix>922`.
- Signing adds these constant fields [rec, byte-exact]: `_client_type=2`, `_client_version=7.0.0.0`,
  `_phone_imei=161177100287274`, `from=mini_ad_wandoujia`, `model=LG-H818`,
  `cuid=0C92F544C09528D294B3857079A22950|472782001771161`, and then `sign`.
  ```
  imei  = sumIMEI("LG-H818_")             # h=53202347234687234; for each byte c: h += (h<<5) + c  (uint64 wrap)
                                          # h %= 1e15; if h < 1e14: h += 1e14   -> 161177100287274
  cuid  = UPPER(md5hex("_" + "7.0.0.0" + "_" + imei + "_" + "mini_ad_wandoujia")) + "|" + reverse(imei)
  sign  = UPPER(md5hex( concat over keys sorted asc of (key + "=" + rawValue) + "tiebaclient!!!" ))
  ```
  (Values are **raw**, not URL-encoded. `sign` itself is excluded from the concatenation.)
- Response [rec]: `{"user":{"id":"<uid>","name":"<name>","BDUSS":"...","portrait":"..."},"anti":{"tbs":...},"error_code":"0",...}`.
  `user.id` is a **string** here. Error: `error_code != "0"` → `检测BDUSS有效性错误代码: <code>, 消息: <error_msg>` (auth).
- If `user.id` is empty but `user.name` is set: `GET http://tieba.baidu.com/home/get/panel?un=<name>` → `data.id`
  (unverified — from old code).
- Config `force_login_username` bypasses tieba entirely (uid=24 placeholder) (unverified — from old code).

### 1.2 Tieba profile: name, sex, age [rec]
`GET http://c.tieba.baidu.com/c/u/user/profile?has_plist=0&need_post_count=1&rn=1&uid=<uid>&sign=<S>`
- `S = UPPER(md5hex(queryWithoutSign.replace("&","") + "tiebaclient!!!"))`. Verified: uid <uid> →
  `634B88F1C09BA6E4DC291720A498B6D1`. `sign` is appended **last**.
- UA: BROWSER_UA [rec].
- Used fields: `user.name` (username), `user.name_show`, `user.sex` (1 ♂, 2 ♀, else unknown), `user.tb_age` (a
  string, e.g. `"13.6"`). Here `user.id` is a **number**.
- `login` makes exactly these two calls [rec basic/01]. `who` makes **no** network call: it prints uid, name, sex and age
  from config (after `logout` it prints `uid: 0`) [rec].

### 1.3 `GET /api/user/getinfo`: uk (user key) [rec]
`GET https://pan.baidu.com/api/user/getinfo?need_selfinfo=1`. UA: empty (PCS_UA) [rec]. Envelope: errno.
- Response: `records[0].uk` (e.g. <uk>), plus `uname`, `nick_name`, `vip_type` (2 = SVIP), `vip_level`,
  `avatar_url`. Old code requires `len(records)==1`.
- Used only to compute the precreate `data_offset` (§4.3). It is cached per BDUSS for 24h. On error, upload fails with
  `获取用户uk错误, 请确保登录信息包含了STOKEN`.
- `uk` also appears in the share page (`uk`, `share_uk`) and in download URLs (`vuk`).

### 1.4 bdstoken
- Where it is used: `share/verify` (form), `share/list` (query), `share/transfer` (query).
- Where it comes from (only path in use): the **share page HTML** (`window.locals.bdstoken`, §8.1). It is the
  *logged-in viewer's* token. It is present only when logged in with a pan STOKEN.
- Alternative (unused dead code): `GET /api/gettemplatevariable?clienttype=0&app_id=<APPID>&fields=["bdstoken"]` →
  `result.bdstoken` (unverified — from old code).

### 1.5 Browser cookie import (macOS; conceptual)
1. Locate the cookie DB: Chrome `~/Library/Application Support/Google/Chrome/<Profile>/Network/Cookies` (fallback
   `<Profile>/Cookies`). Edge: `.../Microsoft Edge/<Profile>/...`. Profile defaults to `Default`. Reading needs "Full
   Disk Access" for the terminal.
2. Keychain password: `/usr/bin/security find-generic-password -w -s "Chrome Safe Storage" -a "Chrome"` (Edge:
   `"Microsoft Edge Safe Storage"` / `"Microsoft Edge"`).
3. Key = `PBKDF2-HMAC-SHA1(password, salt="saltysalt", iter=1003, len=16)`.
4. Copy the DB to a temp file (it is locked while the browser runs), then
   `SELECT host_key,name,value,hex(encrypted_value) FROM cookies WHERE host_key LIKE '%baidu.com'`. The old code shells
   out to `sqlite3 -readonly -json`.
5. If `value` is empty, decrypt `encrypted_value`: it must start with `v10`. Use AES-128-CBC with IV = 16 spaces
   (0x20) and PKCS#7 unpadding. Newer Chrome prefixes the plaintext with
   `SHA256(host_key)` (32 bytes); the old code strips that prefix when present.
6. Keep only hosts `pan.baidu.com`, `.pan.baidu.com`, `.baidu.com` and `baidu.com`, **in that priority order**.
   Dedupe by name, first wins. This matters because the pan STOKEN (on `pan.baidu.com`) differs from the passport
   STOKEN. Take the first non-empty `BDUSS` and `STOKEN`. The cookie header is `name=value; ...` over the deduped list.
7. Old CLI: it passes the full cookie header, **not** the separate STOKEN, so the client uses the full jar. If there is
   no pan STOKEN it warns `警告: 未找到网盘的 STOKEN, 转存功能将不可用...`.

---

## 2. Quota [rec]
`GET https://pcs.baidu.com/rest/2.0/pcs/quota?app_id=266719&method=info`. UA empty.
Response `{"quota":21009906270208,"used":6364689840571}` (bytes). free = quota − used.

---

## 3. File metadata and management (PCS + pan list)

### 3.1 File object fields
| field | meta/search (PCS) | list (pan) | notes |
|---|---|---|---|
| `fs_id` int64 | ✓ | ✓ | |
| `path`, `server_filename` | ✓ | ✓ | pan list escapes `/` as `\/` in JSON (normal JSON) |
| `isdir` 0/1 | ✓ | ✓ | |
| `size` | ✓ | ✓ | 0 for dirs |
| `md5` | ✓ (**obfuscated**) | ✓ (obfuscated) | see 3.7; absent for dirs |
| `block_list` []string | ✓ (files) | ✗ | real per-block md5s |
| `app_id` | ✓ | ✗ (old tool shows 0) | |
| `ctime`,`mtime` | ✓ (used by old code for meta/search) | – | |
| `server_ctime`,`server_mtime` | ✓ | ✓ (used for list) | |
| `local_ctime`,`local_mtime` | ✓ | ✓ | |
| `ifhassubdir` | ✓ dirs | – | |
| `category` | ✓ | ✓ | 6 = other/dir, 4 = doc… |

### 3.2 meta (PCS batch) [rec]
`POST https://pcs.baidu.com/rest/2.0/pcs/file?app_id=266719&method=meta`, multipart param
`{"list":[{"path":"/a"},{"path":"/b"}]}`.
- Success: `{"list":[{...file...}]}`.
- Missing path: **HTTP 404** `{"error_code":31066,"error_msg":"file does not exist"}` [rec]. In a batch, one missing
  path fails the whole call (unverified).
- Root `/` is special-cased client-side as a directory by the old code (`IsDir("/")` makes no call).
- Used by: `meta`, `cd` (check that the target is a dir), `cp`/`mv` (check whether the destination exists or is a
  dir), `download` (walk root), upload pre-check (`checkTarget`), and `locate --pan`.

### 3.3 list (pan) with pagination and ordering [rec]
`GET https://pan.baidu.com/api/list?dir=<dir>&order=<name|time|size>&desc=<0|1>&clienttype=0&num=1000&page=<n>`
- UA NETDISK_UA. Envelope errno.
- `page` starts at 1. Loop: stop when `len(list) < 1000`. The old code also stops if a page repeats an already-seen
  `fs_id` (the guard against the server ignoring `page`), with a hard cap of 2000 pages.
- **Missing dir**: `{"errno":-9}` with HTTP 200 [rec]. An empty dir gives `{"errno":0,"list":[]}`.
- The old code chose this over PCS `file?method=list`, which is capped at 1000 and ignores paging (unverified).
- Directory ordering follows the server; with `order=size` the dirs have size 0 [rec misc/07].
- Used by: `ls`, `tree` (recursive, one call per dir), `download` (walk), glob expansion (list the parent, match the
  basename with shell `* ? [ ]` client-side), and the upload existence check (cached 1 min per dir).

### 3.4 search (PCS) [rec]
`GET https://pcs.baidu.com/rest/2.0/pcs/file?app_id=266719&method=search&path=<dir>&wd=<keyword>&re=<0|1>`
→ `{"list":[file...]}` (same shape as meta, including `block_list`). `re=1` is recursive.

### 3.5 mkdir, delete, copy, move (PCS) [rec]
- **mkdir**: `POST .../pcs/file?app_id=266719&method=mkdir&path=<p>` with no body. Parents are created implicitly
  (`/bnd-test/x/y` in one call) [rec]. Success:
  `{"ctime","fs_id","isdir":1,"mtime","path","status":0}`. Already exists: **HTTP 400**
  `{"error_code":31061,"error_msg":"file already exists"}` [rec].
- **delete** (to the recycle bin): `POST ...method=delete`, param `{"list":[{"path":...},...]}` → `{"request_id":...}`.
  Missing: **404 / 31066** [rec]. The old CLI expands globs first, then makes one batch call.
- **copy**: `POST ...method=copy`, param `{"list":[{"from":"/a","to":"/b/a"},...]}` →
  `{"extra":{"list":[{"from","from_fs_id","to","to_fs_id"}]}}` [rec].
- **move / rename**: `POST ...method=move`, same param → `{"extra":{"list":[{"from","to"}]}}` [rec]. Rename = move
  within the same dir.
- `to` is the **full destination path**, not a directory. The CLI resolves "into dir" semantics first with `meta(dest)`:
  if dest is an existing dir, `to = dest/basename(from)`. With 1 source and a nonexistent dest, it does copy/rename to
  that exact path [rec].
- Batch limit: pan errno `-33` "一次支持操作999个" (unverified).

### 3.6 Recycle bin (unverified — from old code)
- list: `GET https://pan.baidu.com/api/recycle/list?num=100&page=<n>`, UA netdisk, errno →
  `list[]{fs_id,isdir,leftTime(days),path,server_filename,server_ctime,server_mtime,md5,size}`.
- restore: `POST https://pcs.baidu.com/rest/2.0/pcs/file?app_id=<APPID>&method=restore`, param
  `{"list":[{"fs_id":N},...]}` → `extra.list[].fs_id` (the ones restored). PCS envelope.
- delete (permanent): `POST https://pan.baidu.com/api/recycle/delete`, form `fidlist=[N,...]`, UA netdisk, errno.
- clear: `GET https://pcs.baidu.com/rest/2.0/pcs/file?app_id=<APPID>&method=delete&type=recycle` → `extra.succNum`.

### 3.7 MD5 obfuscation ("md5 decryption") [rec-verified]
The server returns obfuscated md5s in `md5` fields (meta, list, search, precreate/create responses, and CDN URL paths
`/file/<enc>`). `block_list` entries are **not** obfuscated. Algorithm (verified on 4 recorded pairs):
```
decrypt(raw):
  if len(raw) != 32 or raw[9] in [0-9a-f]: return raw          # not obfuscated
  s = raw[0:9] + hex1(ord(raw[9]) - ord('g')) + raw[10:]        # 'g'..'v' -> 0..f
  o = for i in 0..31: hex1( hexval(s[i]) XOR (i & 15) )
  return o[8:16] + o[0:8] + o[24:32] + o[16:24]
```
Example: `d8319ac05p986c5910e98c966b7d1f9d` → `d033a1b6d912dfa7e2d6d27211cac9f1` (= md5("hello, baidupcs\n")).
The old code also applies: `if len(block_list)==1: md5 = block_list[0]` (then decrypt, a no-op).

**What the decrypted md5 means (important):**
- For a file uploaded in **one block** (≤ 4 MiB), or 秒传'd, it is the true content md5.
- For a **multi-block** chunked upload, the stored md5 is **`md5(<the create block_list JSON string>)`**. Verified:
  big.bin (5 blocks): meta md5 decrypts to `ef538a4e…` = `md5('["dc48…","e834…","223d…","2df6…","b447…"]')`, while the
  real content md5 is `7f9be07a…`. The old tool prints `md5 (可能不正确)` when `len(block_list) != 1`, skips post-download
  md5 verification for such files, and had a `fixmd5` feature to repair this (§10).
- The **CDN download response header `Content-MD5`** is the true content md5 even for multi-block files [rec large/07:
  `Content-Md5: 7f9be07a…`, `Superfile: 2`]. Single-block files show `Superfile: 0`. The new implementation can use
  this for download verification.

---

## 4. Upload (chunked, with rapid-upload attempt)

### 4.1 Constants
- Block size is chosen **by file size, not by VIP level**: 4 MiB; 16 MiB if size ≥ 8 GiB; 64 MiB if size ≥ 32 GiB.
  Max file 128 GiB (refused above that); warning at ≥ 32 GiB. The VIP-based limits (4/16/32 MiB for
  normal/VIP/SVIP) in Baidu's open-platform docs are **not** used by the old code. The recorded account is SVIP
  (vip_type 2) and 4 MiB blocks worked [rec large/03].
- `slice-md5` = md5 of the first 256 KiB (whole file if smaller).
- `data_content` window = 4096 bytes.
- Quota pre-check: if size > 64 MiB, call quota; skip the file if free < size.
- Empty files: **the old tool silently skips 0-byte files** (its local walk drops size==0 even for explicit file args).
  Reported: uploading a 0-byte file fails with errno 2 (not in recordings; the old code would send
  `block_list=[""]`, `content-md5=d41d8cd98f00b204e9800998ecf8427e`).

### 4.2 Sequence for one new file [rec basic/06, large/03]
```
1. GET  /api/list?dir=<parent>...        (cached 1 min; errno -9 == parent missing, OK)
   - same name exists: policy skip -> skip; else if decrypt(md5)==local md5 -> skip ("已存在, 跳过")
2. GET  /api/user/getinfo?need_selfinfo=1   -> uk (cached 24h)
3. compute: content-md5, slice-md5, block md5 list, data_offset/data_content (4.3)
4. POST pcs file?method=meta [target]       -> 31066 expected; dir -> refuse ("保存路径不可以覆盖目录");
                                               exists -> skip (policy skip) / skip if same size (rsync) / continue (overwrite)
5. POST /api/precreate  (rapid attempt)     -> return_type 2: done (秒传成功) | return_type 1: uploadid
6. GET  pcs file?method=locateupload         -> choose upload host
7. POST https://<host>/rest/2.0/pcs/superfile2?...   (one per block, parallel, any order)
8. POST /api/create                          -> file created
```
Parent directories are created implicitly by precreate/create (`/bnd-test/dir/sub/b.txt` was uploaded without a
mkdir) [rec].

### 4.3 precreate, rapid attempt (the "RapidUpload" form) [rec]
`POST https://pan.baidu.com/api/precreate` (no query). Headers: `Content-Type: application/x-www-form-urlencoded`,
`Accept: */*`, `Connection: keep-alive`, UA NETDISK_UA. Envelope errno.

| field | value |
|---|---|
| `path` | full target path |
| `target_path` | `dirname(path) + "/"` (trailing slash) |
| `size` | bytes |
| `isdir` | `0` |
| `autoinit` | `1` |
| `rtype` | `2` for policy skip, `3` for overwrite/rsync (old mapping; see note) |
| `checkexist` | `0` |
| `mode` | `1` |
| `local_mtime`, `local_ctime`, `data_time` | the same unix seconds `T` (current time, **not** the file mtime) |
| `content-md5` | lowercase hex md5 of the whole file |
| `slice-md5` | lowercase hex md5 of the first 256 KiB |
| `block_list` | JSON array of per-block md5s, e.g. `["ea2e…"]` |
| `data_offset` | see below |
| `data_length` | bytes actually read (≤ 4096) |
| `data_content` | base64 (std alphabet, **`=` padding stripped**) of `file[data_offset : data_offset+4096]` |
| `uploadid` | only when resuming an earlier session |

```
data_offset(uk, contentMD5hex, T, size):
  h = md5hex( decimal(uk) + contentMD5hex + decimal(T) )
  raw = int(h[0:8], 16)
  if size - 4096 + 1 <= 1: return 0
  return raw mod (size - 4096 + 1)
```
Verified: uk=<uk>, md5=ea2e4a9d…, T=1790356507, size=65536 → 30714 [rec].

Responses [rec]:
- Needs upload: `{"errno":0,"return_type":1,"uploadid":"N1-…","block_list":[0,1,2,3,4],"path":"…"}`.
  `block_list` holds the **indices** the server wants. The old code ignores it and uploads every block.
- Rapid success (秒传): `{"errno":0,"return_type":2,"info":{"md5":"<enc>","fs_id":…,"size":…,"path":…,"ctime","mtime","category","isdir":0}}`.
  Observed for tiny common content (`"a\n"`, `"file a\n"`) [rec misc/05, basic/06]. So **content-md5 dedup still
  works** whenever Baidu already holds the content. What is dead is 秒传 *links/commands* (md5-only transfers), §14.
- Errors: 31112 exceed quota (from old code); others unverified.

**rtype note:** Baidu's open-platform docs define rtype 0 = fail on conflict, 1 = rename, 2 = rename only if the
block_list differs, 3 = overwrite. The old "skip ⇒ 2" is therefore really "rename if different". Skip semantics come
from the client-side pre-check (steps 1 and 4), not from the server. (The docs semantics are unverified here.)

### 4.4 precreate, no-rapid variant (`--norapid`) (unverified — from old code)
`POST https://pan.baidu.com/api/precreate?app_id=250528&channel=1&web=1`, form `path`, `target_path=dirname/`,
`local_mtime=<now>`, `autoinit=1`, `rtype`, and a fake
`block_list=["5910a591dd8fc18c32a8f3df4fdc1761","a5fc157d78e6ad1c7e114b056c92821e"]` (only the first when size ≤ 4 MiB).
Returns `uploadid`.

### 4.5 locateupload: pick an upload host [rec]
`GET https://pcs.baidu.com/rest/2.0/pcs/file?app_id=250528&method=locateupload&upload_version=2.0` (UA empty).
Response: `{"error_code":0,"host":"c.pcs.baidu.com","servers":[{"server":"https://bjdd-ct11.pcs.baidu.com"},{"server":"https://c7.pcs.baidu.com"},{"server":"http://bjdd-ct11.pcs.baidu.com"},…],"bak_servers":[…],"quic_servers":[…],"expire":60,…}`.
Old selection: keep the `servers[]` entries whose URL contains `-` (regional nodes such as `bjdd-ct11`, `xafj-ct11`),
take the hostname, and pick one at random. Always use the configured scheme. Fallback: `pcs_addr` or `pcs.baidu.com`.
Called once per file, and again every 256 parts. `fix_pcs_addr` disables the lookup.

### 4.6 superfile2: upload one block [rec]
`POST https://<host>/rest/2.0/pcs/superfile2?app_id=266719&method=upload&type=tmpfile&path=<target>&uploadid=<id>&partseq=<i>&partoffset=<i*blocksize>&vip=1`
- Body: multipart `uploadedfile` (filename `""`) holding the block bytes. UA empty. Cookies required.
- Response: `{"md5":"<block md5>","request_id":…}`, plus headers `Content-MD5` and `x-bs-meta-crc32`. The md5 is
  plain, not obfuscated.
- Blocks are sent in parallel, in any order (recorded partseq order 0,1,3,2,4) [rec]. Old client timeout: 200 s per
  request.
- Errors: 4xx/5xx → PCS error. 400/401/403/413 are terminal for that part. `31363` "block miss in superfile2" means
  the session expired: drop the resume state and start over (from old code).

### 4.7 create: merge blocks [rec]
`POST https://pan.baidu.com/api/create`, form: `path`, `size`, `isdir=0`, `rtype` (same as precreate), `uploadid`,
`block_list` (JSON array of the md5s returned by superfile2, **ordered by partseq**), and
`target_path=dirname(path)` (**no** trailing slash here). UA NETDISK_UA, errno.
Response: `{"errno":0,"fs_id","md5":"<enc>","path","server_filename","size","ctime","mtime","category","name":path}`.
Possible error 31061 (target exists) is handled as "exists / skip" (from old code).

### 4.8 Resume
The old code persists `{uploadid, per-block md5 state}` keyed by local file (path + size + mtime) in
`pcs_uploading.json`. On resume it re-runs precreate with `uploadid=` and re-uploads missing blocks.

---

## 5. Download link acquisition

### 5.1 locatedownload: the default "locate" mode [rec]
`POST https://pcs.baidu.com/rest/2.0/pcs/file?<params>` with **no body**. UA **NETDISK_UA** (required: the wrong UA
gives "user is not authorized, hitcode:…", PCS error 31626 per old notes). Cookies required.

Wire order of the query string (the old code builds this raw string; the signature params **must come last** and must
not be re-sorted):
```
ant=1&apn_id=1_0&app_id=250528&channel=0&check_blue=1&clienttype=17&es=1&esl=1&freeisp=0&method=locatedownload&path=<urlenc>&queryfree=0&use=0&ver=4.0
  &time=<T>&rand=<R>&devuid=<D>&cuid=<D>
```
(The first part is Go's `url.Values.Encode()`, i.e. sorted keys. The recording shows everything re-sorted by the
cassette.)

Signature:
```
D (devuid = cuid) = UPPER(md5hex(BDUSS)) + "|0"                 e.g. E72209D82DD4545A8975424458073A5F|0
T = unix seconds
R = sha1hex( sha1hex(BDUSS) + decimal(uid) + SECRET + decimal(T) + D )
SECRET = "ebrcUYiuxaZv2XGu7KIYKxUrqfnOfpDF"   (32 ASCII bytes)
```
- `uid` is the **Baidu uid** from tieba (e.g. <uid>), not the pan `uk`. The old code re-runs tieba login when uid is
  0.
- Test vector (old unit test): BDUSS=`test_bduss`, uid=10086, T=1571140066, D=`O|1E67351CCE80B2CF48DB511CD77ACD9F` →
  R=`b6bb7a6f46899e181baea58798d4fdb889775c2c` (re-verified with Python).
- `|` in D is URL-encoded as `%7C` [rec].

Response [rec]: `{"timestamp":…,"client_ip":"…","urls":[{"url":"https://xafj-ct11.baidupcs.com/file/<enc-md5>?bkt=…&fid=<uk>-250528-<fsid>&time=…&sign=…&expires=8h&…","rank":1,"encrypt":0},…]}`.
There are usually about 6 URLs on different nodes (`xafj-ct11`, `bdd0`, `allall02`, `allall06`, `yqd0`, …). Links
expire after 8h.

Old URL choice/failover:
1. Drop entries with `encrypt != 0`. Force the scheme to the configured one.
2. Take `urls[dindex]` (`--dindex`, default 0, clamped). If its host starts with `nb.cache` and another exists, take
   the next one.
3. There is **no** per-request failover across the URL list. On a finished-file size mismatch, `dindex` is incremented
   and the task retries (up to 3). Every retry re-calls `locatedownload`. HTTP 403/404/416 from the CDN are terminal
   for the worker.
4. The rewrite would do better with real failover across `urls[]` on 403/5xx/timeouts.

### 5.2 CDN range GETs [rec]
- Headers: `User-Agent: NETDISK_UA`, `Range: bytes=a-b`, and cookies (copied from the jar). No Referer.
- Probe: first `GET` with `Range: bytes=0-32767`. The total size is read from the **`x-bs-file-size`** response header
  [rec]; `Content-Range: bytes 0-32767/<total>` also works. Other useful headers: `Content-MD5` (true md5),
  `x-bs-meta-crc32`, `Content-Disposition: attachment;filename="…"`, `Accept-Ranges: bytes`, `Superfile: 0|2`.
- Then parallel ranges. The old downloader splits into blocks (≥ 256 KiB, up to 4 MiB; with `-p 4` on 20 MiB it made 5
  × 4 MiB ranges) [rec large/07]. It checks that the `Content-Range` total equals the expected size. 206 expected; 200
  accepted.
- Status handling (old worker): 200/206 OK; 403/404/416 → fatal; 406 → retry; 429/509 → too many connections (back
  off); anything else → retry.
- Known "banned file" placeholders (content replaced by Baidu): size 1749504 md5 `48bb9b0361dc9c672f3dc7b3ffcfde97`
  (8秒温馨提示) and size 120 md5 `6c1b84914588d09a6e5ec43605557457`.
- 0-byte files: no request; just create the empty file. Directories: `meta` + recursive `list`, then mkdir locally.

### 5.3 PCS download: `--mode pcs` / `stream` [rec]
`GET https://pcs.baidu.com/rest/2.0/pcs/file?app_id=266719&method=download&path=<p>` (`stream` mode uses
`/rest/2.0/pcs/stream?...`, unverified).
- UA empty, cookies, `Range` as usual.
- Response: **302** with `Location: https://qdall01.baidupcs.com/file/<enc>?…&fid=<uk>-266719-<fsid>…` and body
  `{"error_code":302,…}`. **Do not treat that error_code as a failure.**
- Follow the redirect. The redirected request carries `Referer: <pcs url>`, no Cookie, and the empty UA → 206 [rec]. The
  old client keeps the Referer only under https; it deletes the Referer on redirect when https is off.
- Every range request goes through its own 302 (no caching of the Location) [rec].
- Old notes: rate-limited; files > ~3.9 GB may not work (unverified).

### 5.4 Web download link: `locate --pan` [dead?] (unverified — from old code)
1. `GET https://pan.baidu.com/disk/home` with UA `Mozilla/5.0`, cookies, redirects **not** followed.
   `Location: /` or passport.baidu.com means the cookie is invalid.
2. Regex over the HTML: `"sign1":"(.*?)"[\s\S]*"sign3":"(.*?)","timestamp":(\d*?),`.
3. `sign = base64(RC4like(key=sign3, data=sign1))`, where `Sign2` is a textbook RC4 KSA/PRGA over runes (key scheduled
   from sign3 repeated to 256; output = data XOR keystream). Cache it for 1h.
4. `POST https://pan.baidu.com/api/download`, form `sign`, `timestamp`, `fidlist=[fsid,…]`, UA netdisk → `dlink[]{fs_id,dlink}`.
   errno 112 (页面已过期) or 113 (签名错误) invalidates the cached sign.
   Today's `/disk/home` is an SPA, so the regex very likely fails ("网盘首页数据匹配出错").

---

## 6. Sharing (own shares)

### 6.1 Create a share: `share/pset` [rec]
`POST https://pan.baidu.com/share/pset`, form (UA NETDISK_UA, errno):
`path_list=["/p1","/p2"]`, `schannel=4`, `channel_list=[]`, `period=<days, 0=forever>`, `pwd=<4 chars>`, `share_type=9`.
- No bdstoken is needed [rec].
- Old: if pwd is not 4 chars, generate `md5hex("Asswecan"+now)[:4]`. The server requires `pwd`; arbitrary periods such
  as 1 are accepted.
- Response [rec]: `{"errno":0,"shareid":54544028961,"link":"https://pan.baidu.com/s/1kFbKs7h79vbuLic07mOwgw","shorturl":…,"expiretime":<unix>,"expiredType":1,"ctime",…,"show_msg":""}`.
  An empty `link` is an error.
- Output format in the old tool: `shareID: <id>, 链接: <link>, 密码: <pwd>`. `-f` prints `<link>?pwd=<pwd>`.
- Errors from the table: 110 share-count limit, 115 / -16 forbidden file, -70 virus, 108 sensitive name,
  -1 sharing banned.

### 6.2 Cancel shares: `share/cancel` [rec]
`POST https://pan.baidu.com/share/cancel`, form `shareid_list=[id,…]` → `{"errno":0,"show_msg":""}`.

### 6.3 List own shares: `share/record` (unverified — from old code)
`GET https://pan.baidu.com/share/record?page=<n>&desc=1&order=time`, UA netdisk, errno.
- `list[]`: `shareId`, `fsIds[]`, `shortlink`, `status`, `public` (1 public / 0 private), `typicalPath`,
  `typicalCategory`, `expiredType` (-1 = expired), `expiredTime` (seconds left; 0 = forever), `vCnt` (views).
- With no shares, `list` is `{}` (an object, not an array). Handle both.
- Private-share password: `GET https://pan.baidu.com/share/surlinfoinrecord?shareid=<id>&sign=<md5hex(decimal(shareid)+"_sharesurlinfo!@#")>`
  → `pwd` (`"0"` = none). The old CLI calls it for every private, non-expired record.

---

## 8. Share-link transfer (转存) [rec share-transfer/07–09]

### 8.0 Link parsing (old rules)
- Accept free text. Regex `(https?://pan\.baidu\.com/s/[0-9A-Za-z_-]+)(?:\?pwd=([0-9A-Za-z]{4}))?`. An explicit pwd
  argument wins over `?pwd=`.
- `surl` = last path segment (starts with `1`). For `/share/init?surl=X`, surl = `"1"+X`.
- `shorturl` (used in APIs) = surl without its leading `1`.
- Old validation: `len(surl) ≤ 23`, `surl[0]=='1'`, and **pwd must be exactly 4 chars**. So password-less public shares
  cannot be transferred by the old tool (gotcha / bug). Links containing `bdlink=` or not on `pan.baidu.com` fail with
  `秒传已不再被支持`.

### 8.1 Step 1: share page
`GET https://pan.baidu.com/s/<surl>`, headers `User-Agent: NETDISK_UA` (**a browser UA redirects to a logged-out
page**, per old comment #534), `Referer: https://pan.baidu.com/disk/home`, cookies.
- HTML 200 [rec]. It contains
  `try{ window.locals = {…}; }catch(ex){…}` (note that an earlier `window.locals = {};` also exists). Used keys [rec]:
  `bdstoken` (32 hex, the viewer's token), `shareid` (number, e.g. 17666619262), `share_uk` (string, the sharer's uk),
  `uk` (the viewer's uk), `loginstate` (1), `self` (1 = your own share), `errno`, `errortype`, `linkusername`,
  `sharetype`.
- Old extraction: regex `(\{.+?loginstate.+?\})`, then lenient JSON (gjson). Rewrite: find `window.locals = ` followed
  by `{` that is not `{}` and decode one JSON object with a streaming decoder.
- No `loginstate` → `请确认登录参数中已经包含了网盘STOKEN` (auth).
- The body contains `error-404` → `页面不存在`; `platform-non-found` → `分享链接已失效` (input) (unverified).

### 8.2 Step 2: verify the password
`POST https://pan.baidu.com/share/verify?shareid=<shareid>&time=<unix ms>&clienttype=1&uk=<share_uk>`
- Headers: BROWSER_UA, `Content-Type: application/x-www-form-urlencoded; charset=UTF-8`,
  `Referer: <the normalised share link, https://pan.baidu.com/s/<surl>>`, cookies.
- Form: `pwd=<4 chars>&vcode=null&vcode_str=null&bdstoken=<bdstoken>`.
- Response (the body starts with `\n\n`) [rec]: `{"errno":0,"err_msg":"","randsk":"yGY%2Beg…%3D"}` and `Set-Cookie`
  **`BDCLND=<randsk>`**, which must be stored in the jar. A wrong password gives `{"errno":-9}` [rec] → `提取码错误`
  (input). Other errnos: `-62`/`-19` captcha required (unverified).
- The old code then **dedupes the cookie jar keeping the newest value per name**.

### 8.3 Step 3: share page again
The same GET as 8.1, now with `Referer: https://pan.baidu.com/share/init?surl=<shorturl>` [rec], to get the
post-verify locals (`bdstoken`, `shareid`, `share_uk`).

### 8.4 Step 4: list the share root
`GET https://pan.baidu.com/share/list?bdstoken=<t>&root=1&web=5&app_id=250528&shorturl=<shorturl>&channel=chunlei`
- Headers: BROWSER_UA, `Content-Type: application/x-www-form-urlencoded; charset=UTF-8`, cookies (with BDCLND).
- Response [rec]: `{"errno":0,"title":"/bnd-test/shared","list":[{"fs_id":"966229782204241","isdir":"1","path":"/bnd-test/shared","server_filename":"shared","size":"0","md5":"",…}],"cur_total":1,"share_id":17666619262,"uk":<uk>,"expired_type":1,"show_msg":"success"}`.
  **Numbers are strings** in `list[]`.
- The old code uses only the root items: `fs_id` for all of them and `server_filename` of the first. It does no
  pagination and no sub-dir listing. errno 8001 → `已触发验证, 请稍后再试`.

### 8.5 Step 5: transfer
`POST https://pan.baidu.com/share/transfer?app_id=250528&channel=chunlei&clienttype=0&web=1&filename=<first name>&shareid=<shareid>&from=<share_uk>&bdstoken=<t>`
- Headers: `User-Agent: TRANSFER_UA` (Chrome 76 / Windows), `Referer: https://pan.baidu.com/s/<surl>`,
  `Content-Type: application/x-www-form-urlencoded`, cookies.
- Form: `fsidlist=[966229782204241,…]` (JSON ints), `path=<destination dir>` (the old CLI uses the current workdir).
- `--fs_id X` overrides `fsidlist=[X]`. `--collect` with more than one item first mkdirs `<dir>/<first name>等文件`
  and transfers into it.
- Success (unverified shape): `{"errno":0,"info":[{"path":"/dest/name","fsid":…,"errno":0}],…}`. The old code shows
  `basename(info[0].path)`, plus `等多个文件/文件夹` if there is more than one.
- Errors:
  - **errno 2, `show_msg:"文件已存在"`**, returned when transferring **your own share** even into an empty dir [rec: dest
    `/bnd-test/in` was empty]. The old CLI prints `分享链接转存到网盘失败: 文件已存在 (错误码 2)` with exit 1.
  - errno 4 → `文件重复`.
  - errno 12 → look at `target_file_nums` vs `target_file_nums_limit` (too many files:
    `转存文件数%d超过当前用户上限, 当前用户单次最大转存数%d`), or `info[0].errno == -30` (a same-name item exists in
    the dest).
  - Others: use `show_msg` if present, else `获取分享项元数据错误 (错误码 N)`.
  - Table entries relevant here: 105 bad link, -7 / -21 share cancelled, -33 over 999 items, 132 security check.

---

## 9. Offline download (cloud_dl) (unverified — from old code)
Base: `https://pan.baidu.com/rest/2.0/services/cloud_dl?method=<m>&app_id=250528&...`. All parameters go in the
**query**, the POST has no body, the UA is empty (PCS_UA), and the envelope is PCS-style (`error_code`/`error_msg`).

| op | method | HTTP | extra query | response |
|---|---|---|---|---|
| add | `add_task` | POST | `task_from=0&selected_idx=1&save_path=<dir>&source_url=<url or magnet>` | `task_id` (number) |
| query | `query_task` | GET | `op_type=1&task_ids=<id,id,…>` (≤100) | `task_info: {"<id>": {…}}` |
| list | `list_task` | POST | `need_task_info=1&status=255&start=0&limit=1000` | `task_info: [ {…task_id…} ]` |
| cancel | `cancel_task` | POST | `task_id=<id>` | – |
| delete | `delete_task` | POST | `task_id=<id>` | – |
| clear | `clear_task` | POST | (the old code omits app_id) | `total` |

Task fields: **all numbers are strings**: `status`, `task_id`, `file_size`, `finished_size`, `create_time`,
`start_time`, `finish_time`, `save_path`, `source_url`, `task_name`, `od_type`,
`file_list[]{file_name,file_size}`, and `result` (int: 0 ok, 1 not found).
Status: 0 下载成功, 1 下载进行中, 2 系统错误, 3 资源不存在, 4 下载超时, 5 资源存在但下载失败, 6 存储空间不足, 7 任务取消.

---

## 10. Rapid-upload info / export / fixmd5 [dead?] (unverified — from old code)
- **RapidUploadInfo(file)**: if size ≤ 256 KiB and `block_list==[md5]`, use the md5 directly. Otherwise locatedownload
  → CDN GET with UA netdisk and `Range: bytes=0-262143` → read `Content-MD5`, `Content-Disposition` filename,
  `Content-Range` total and `x-bs-meta-crc32` (missing or "0" → error), and md5 the first 256 KiB for `slice-md5`. If
  `Content-MD5` is missing and size < 4 GB, retry with the PCS download URL.
- **export**: prints `bnd rapidupload -length=… -md5=… -slicemd5=… -crc32=… "<path>"` lines, or
  `md5#slicemd5#size#name` links. **No `rapidupload` command exists any more**, so this output is unusable (help text
  says 秒传已经失效).
- **fixmd5**: rapid-create over the existing file with the true md5:
  `POST https://pan.baidu.com/rest/2.0/xpan/file?method=create&access_token=<token>`, form `block_list=["<true md5>"]`,
  `path`, `size`, `isdir=0`, `rtype=3`. It needs an open-platform `access_token` (hence `setastoken`; errno 9019 when
  unset). The code exists (`runFixMD5`), but **no CLI command is registered**, so it is unreachable.

---

## 11. Other non-pan endpoints (old tool)
- `tool getip`: `https://techain.baidu.com/srcmon` (also `https://api.ipify.org`, `http://mam.netease.com/api/config/getClientIp`).
- `update`: `https://api.github.com/repos/<repo>/releases/latest`.

---

## 12. Error codes → meaning → suggested CLI kind

Old CLI kinds and exit codes: `input`=2, `auth`=4, `failed`=1, `usage`=64, `dependency`=3, `cancelled`=130.
Old classification: auth = {-6, -4, -11, 3, 31045, 9019}; input = {31066, -9, 31061, -8, -30} plus the share
wrong-password / gone / 404 errors; everything else = failed.

| Family | Code | Meaning (server msg / old text) | Where seen | Kind (suggested) |
|---|---|---|---|---|
| PCS | 31066 | file does not exist / 文件或目录不存在 (HTTP 404) | meta, delete [rec] | input |
| PCS | 31061 | file already exists / 文件已存在 (HTTP 400) | mkdir [rec]; create | input |
| PCS | 31045 | user not exists → login expired | any PCS | auth |
| PCS | 31079 | file md5 not found (rapid) / 秒传文件失败 | rapid create | failed |
| PCS | 31112 | exceed quota / 超出配额 | precreate/create | failed (maybe its own "quota" kind) |
| PCS | 31363 | block miss in superfile2: upload session expired | create | failed (restart upload) |
| PCS | 31626 | user is not authorized (wrong UA / hitcode) | locatedownload | failed |
| PCS | 302 | (not an error) body of the pcs download redirect | download [rec] | – |
| Pan | -9 | 文件不存在 (list missing dir) [rec]; **wrong share pwd** in `share/verify` [rec] | list, verify | input |
| Pan | -8 / -30 | 已存在同名文件 / 文件已存在 | file ops, transfer item | input |
| Pan | -6 / -4 / -11 / 3 | 请重新登录 / 登录信息有误 / 验证cookie无效 / 未登录 | any | auth |
| Pan | -7 / -21 | 该分享已删除或已取消 / 分享已取消或分享信息无效 | share | input |
| Pan | -12 | 访问密码错误 | share | input |
| Pan | -19 / -62 | 需要输入验证码 | verify | failed (needs browser) |
| Pan | -33 | 一次支持操作999个 | batch ops, transfer | input |
| Pan | -1 / -10 / -14 / -15 / -16 / -17 / -70 / 108 / 110 / 115 | share-creation refusals (banned, limits, virus, sensitive name, forbidden file) | pset | failed (-16/115/108: input) |
| Pan | 2 | transfer: `show_msg` 文件已存在 (own share) [rec]; old table: 请稍后再试, 或更换保存路径; reported for 0-byte upload | transfer, precreate | failed (or input for own share) |
| Pan | 4 | 文件重复 / 存储好像出问题了 | transfer | input |
| Pan | 12 | transfer partially failed / file-count limit (see §8.5) | transfer | input |
| Pan | 105 | 链接错误没找到文件 | share | input |
| Pan | 112 / 113 | 页面已过期 / 签名错误 | /api/download | failed (refresh sign) |
| Pan | 114 | 当前任务不存在，保存失败 | transfer | failed |
| Pan | 132 | 帐号存在安全风险, 请先进行安全验证 | any | auth |
| Pan | 8001 | 已触发验证, 请稍后再试 | share/list | failed |
| Pan | 9019 | accesstoken 未设置或过期 | xpan create | auth |
| Tieba | ≠"0" | BDUSS invalid | login | auth |
| client | – | 秒传/链接格式非法, 提取码不是4位 | parse | input |
| client | 114514 / 1919810 | old *internal* codes: target exists (skip) / same size (rsync) | upload | (skip, not an error) |

---

## 13. Gotchas (observed)
1. **The PCS UA must be empty**, i.e. no User-Agent header at all [rec: every pcs.baidu.com, superfile2 and quota call].
   The pan API, the share page and locatedownload use the netdisk UA. share verify/list use a Chrome 63 UA; transfer
   uses a Chrome 76 UA.
2. `/api/user/getinfo` works with the empty UA [rec].
3. PCS errors come with non-2xx statuses (404/31066, 400/31061). Pan errors come with HTTP 200. Always decode the body.
4. `list` of a missing dir → `errno -9` (HTTP 200). `meta`/`delete` of a missing path → 404 + 31066. `mkdir` on an
   existing dir → 400 + 31061 [rec].
5. `mkdir` creates parents. precreate/create create parent dirs implicitly [rec].
6. The md5 in meta/list/search/create is obfuscated (§3.7). For multi-block uploads it equals
   md5(block_list JSON), not the content md5. The CDN `Content-MD5` header is the real one.
7. Transferring your **own** share → errno 2 `文件已存在`, even into an empty dir [rec].
8. A wrong share password is `errno -9` (the same number as "file not found") [rec].
9. The `share/verify` body has leading `\n\n` [rec]. The `share/list` `list[]` values are strings [rec]. The
   `share/record` empty list is `{}`.
10. The share page has two `window.locals =` assignments. The first is `{}`.
11. The locatedownload signature params must be the **last** query params, in the order `time, rand, devuid, cuid`.
    The recorded (sorted) order is misleading.
12. `locatedownload` wants the tieba **uid**, not the pan uk. `devuid` is derived from BDUSS.
13. The pcs-mode download body `{"error_code":302}` accompanies a 302. Follow it; do not error.
14. precreate `local_mtime`/`local_ctime` are set to *now*, not the file's mtime (old behaviour).
15. precreate `target_path` has a trailing `/`; create's `target_path` has none [rec].
16. `data_content` is base64 without `=` padding [rec: `aGVsbG8sIGJhaWR1cGNzCg`].
17. The precreate response's `block_list` is a list of **indices** (e.g. `[0,1,2,3,4]`), not md5s.
18. The old tool silently skips 0-byte local files.
19. The upload existence check compares the list md5 with the local md5. That can never match for multi-block remote
    files (see 6), so `--policy rsync/overwrite` re-uploads them. Skip policy skips by name regardless [rec large/06].
20. Uploaded files show `app_id 250528`. Files touched by PCS copy show 266719 [rec].
21. Tiny common content is 秒传'd automatically (return_type 2) [rec]. Do not assume the upload path is exercised in
    tests with tiny files.
22. Cassette: recorded bodies/URLs are normalised. The **bdstoken in the share-page HTML response bodies is NOT
    redacted** in `e2e/testdata/share-transfer/0{7,8,9}-*.jsonl` (`"bdstoken":"<token>"`), and neither is the verify
    `randsk`.

---

## 14. Old CLI commands → endpoints (and status)

| Command | Endpoints | Status |
|---|---|---|
| `login` (-bduss/-stoken, -cookies, -from-chrome/-from-edge) | tieba login + profile (§1.1–1.2); browser import (§1.5) | works [rec] |
| `who` | none (config) | works [rec] |
| `su`, `loglist`, `logout` | none (config) | works [rec: loglist/logout] |
| `setastoken` | none (stores access_token for xpan create) | **obsolete**: only fixmd5 used it, and fixmd5 is unregistered |
| `quota` | pcs quota | works [rec] |
| `cd` | pcs meta | works [rec] |
| `pwd` | none | works |
| `ls` | pan /api/list (paged); glob → list of the parent | works [rec] |
| `tree` | pan /api/list recursive | works [rec] |
| `search` | pcs file search | works [rec] |
| `meta` | pcs meta | works [rec] |
| `match` | pan /api/list (glob expansion) | works (via ls-glob [rec]) |
| `mkdir` | pcs mkdir | works [rec] |
| `rm` | (glob→list) + pcs delete | works [rec] |
| `cp` / `mv` | pcs meta(dest) + pcs copy / move | works [rec] |
| `upload` | list, getinfo, meta, precreate, locateupload, superfile2, create (+quota if >64 MiB) | works [rec]; 秒传 by content works |
| `download` (`--mode locate`) | meta + list (walk), locatedownload, CDN range GETs | works [rec] |
| `download --mode pcs/stream` | meta, pcs download (302 → CDN) | pcs works [rec]; stream unverified |
| `locate` | locatedownload | works [rec] |
| `locate --pan` | GET /disk/home (sign scrape) + POST /api/download | **dead?** (home page is now an SPA) |
| `share set` | share/pset | works [rec] |
| `share cancel` | share/cancel | works [rec] |
| `share list` | share/record + share/surlinfoinrecord | unverified (not recorded on purpose) |
| `transfer` | /s/<surl> ×2, share/verify, share/list, share/transfer (+pcs mkdir for --collect; +download for --download) | works [rec] (only foreign shares succeed; needs 4-char pwd) |
| `transfer --rname` | none | **obsolete** (no-op, "秒传已不再被支持") |
| `offlinedl add/query/list/cancel/delete[-all]` | cloud_dl add/query/list/cancel/delete/clear | unverified |
| `recycle list/restore/delete[-all]` | /api/recycle/list, pcs restore, /api/recycle/delete, pcs delete type=recycle | unverified |
| `export` | list/meta + locatedownload + CDN range GET (rapid info) | **obsolete**: emits `bnd rapidupload …` commands that no longer exist |
| `sumfile` | none (local md5 / slice-md5 / crc32) | local-only; help says 目前秒传功能已失效 |
| `fixmd5` (mentioned in `upload` help) | xpan/file create with access_token | **dead**: not registered as a command |
| `config`, `env`, `run`, `clear`, `quit`, `tool enc/dec/showtime` | none | local |
| `tool getip` | techain.baidu.com/srcmon | non-pan |
| `update` | GitHub releases API | non-pan |

Unused API code in the old client: `BDSToken()` (/api/gettemplatevariable), the no-rapid precreate (only via
`--norapid`), `RapidUploadInfo`/`rapidCreate`.

---

## 15. Open questions for the rewrite
- The 0-byte upload behaviour (errno 2?) is not recorded. Decide between skipping (old behaviour) and a
  precreate/create with `block_list=[]` (open-platform style).
- `share/list` pagination for shares with more than 100 root items, and listing into sub-dirs (`dir=` param), are
  untested.
- `rtype` semantics (2 vs 0) should be confirmed against the live server if skip-by-server is wanted.
- Recycle, offline, share record and `stream` mode have no recordings; record them before relying on this spec.
- Is STOKEN actually required for `/api/user/getinfo` and `share/pset`? The recordings used a full browser cookie jar,
  so it is unknown which cookies were needed.
