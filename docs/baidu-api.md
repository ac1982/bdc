# Baidu Netdisk API, as bdc speaks it

bdc speaks the API of Baidu's **web client** (pan.baidu.com in a browser), the way that client does. Everything here
was observed in a real browser (Playwright, 2026-09-26) and verified with live requests. Where the older API
generations (the Android client's, the PCS REST API, the tieba login that earlier tools used) behave differently,
that is noted; bdc does not use them.

## Identity and common parameters

- **Cookies**: the browser's `.baidu.com` cookies. `BDUSS` is the login; `STOKEN` is needed to open shares logged
  in. Keep one value per name, the newest: after a share's code is entered Baidu sets `BDCLND` on `pan.baidu.com`, and
  a stale `BDCLND` from an imported browser cookie string, sent alongside, makes the share refuse (`share/list` -9,
  the page loops back to the code entry or shows "网盘正在升级"). bdc drops `BDCLND` when logging in.
- **Headers**: a desktop browser `User-Agent`, `Referer: https://pan.baidu.com/disk/main`, `X-Requested-With:
  XMLHttpRequest`, and `Origin: https://pan.baidu.com` on POSTs. The web app sends no CSRF header.
- **Query**: `clienttype=0&app_id=250528&web=1` on every `pan.baidu.com` call. **Every change** (create, file
  manager, upload, share, transfer, recycle, offline) also carries `bdstoken=<token>` in the query.
- **Template variables**: `GET /api/gettemplatevariable?fields=["bdstoken","uk","username",…]` → `result{…}`. This is
  where the token, the user (`uk`, `username`) and the download signature's inputs (`sign1`, `sign3`, `timestamp`)
  come from. A bad login answers `errno -6`. (Earlier tools learnt the uid from a tieba login; nothing needs it now.)
- **Errors**: `errno` (or `error_code` on the few PCS-hosted calls) in the JSON body; HTTP status is not reliable on
  its own. A batch call that fails some items answers `errno 12` with `info[]` holding each item's `{path, errno}`,
  not in request order; the items not named as failed were done.

| code | meaning | bdc's class |
|---|---|---|
| -6 | not logged in / bad login | auth |
| 132 | security check required (`authwidget`, `verify_scene`), see below | auth; the user must verify on the web or phone |
| -9, 31066 | no such file | input |
| -8, -30, 31061 | already exists | input |
| -7 | illegal name | input |
| -12 | wrong extraction code (share/verify answers -9 for it too) | input |
| 4 | share already saved here (`文件已转存`) | input |
| 2 | generic; on share/transfer 文件已存在 (one's own share) or 转存路径不存在 | input when it says 已存在 |
| 404 | rapidupload: content unknown | (upload normally) |

**Security check (132).** Baidu's risk control answers a change (seen on deletes after a burst of them) with `errno 132`
and `authwidget{saferand, safesign, safetpl}` (`safetpl` names the operation, e.g. `filemanager`). The web app then
shows `/disk/appeal?saferand=&safesign=&safetpl=` and calls, each a `POST /api/authwidget?method=<m>` with the common
query and form `safetpl, saferand, safesign`:
1. `get` → `data{sms, email (masked), support_type: ["sms","email"]}`;
2. `send` + `type=sms` (or `email`) → sends a 6-digit code;
3. `check` + `vcode=<code>` → `data{dtoken}`.

The app then repeats the original request unchanged (no dtoken, no new cookie): the check lifts the block for the
account, and later deletes from other clients (bdc) succeed too. Observed 2026-09-26.

## Files

| operation | request | notes |
|---|---|---|
| list | `GET /api/list?dir=&order=name&desc=0&num=1000&page=n` | pages until a short one. A file lists as empty; missing dir -9. Items have no md5. |
| metadata | `GET /api/filemetas?target=<JSON paths>&dlink=0&blocks=1` | `info[]` with `block_list`. A missing path fails the batch (`errno 12`, `info[0].errno -9`, no path). Paths go in the URL: split long lists (servers refuse URLs over ~8 KB). |
| search | `GET /api/search?key=&dir=&recursion=1&num=500&page=n` | matching is fuzzy (`exp9m` finds `export.py`); always searches the whole subtree of `dir` (`recursion` is ignored; bdc filters for a one-level search); `has_more`. |
| mkdir | `POST /api/create?a=commit` form `path, isdir=1, rtype=0, block_list=[]` | creates parents. **Without `rtype` Baidu renames on a clash** (`name_YYYYMMDD_HHMMSS`); `rtype=0` fails with -8. |
| delete / rename / copy / move | `POST /api/filemanager?opera=<op>&async=2&onnest=fail` form `filelist=<JSON>` → `taskid` | delete: `["/p",…]` plus `newVerify=1`; rename: `[{path, newname}]` (can change only the case); copy/move: `[{path, dest, newname}]`, missing `dest` dirs are created. Poll the task (below). **Delete of a missing path reports success**: check with filemetas first. Batches of up to 999 items (bdc uses 500). |
| quota | `GET /api/quota?checkfree=1` | `total`, `used`. |

**Tasks.** `GET /share/taskquery?taskid=` → `status` (`pending`, `running`, `success`, `failed`). A failed task has
`task_errno` (-30 on a clash) and `list[]{from, to, error_code}` naming **only the failed items**; the others were done.
(`async=0` answers synchronously, but a one-item copy onto an existing file there reports `errno 0` with only a
top-level `newno -8`, and nothing is copied; the web app's `async=2` has no such gap.)

**md5.** Baidu's md5 fields are obfuscated (swap the four 8-digit blocks back, XOR each hex digit with its position mod
16; the tenth character, a letter `g`–`v`, marks obfuscation). When `block_list` has exactly one entry it is the content
md5 whatever the size; with several, the stored md5 is not the content's. `list` and `search` carry no `block_list`.
The download response header `Content-MD5` is always the true md5.

## Upload

1. **Instant upload (秒传)**, for files of 256 KiB or more:
   `POST /api/rapidupload?rtype=` form `path, content-length, content-md5, slice-md5, target_path=<dir>/,
   local_mtime, data_time=T, data_offset=O, data_content=<base64 of 256 KiB at O>`. The md5s (whole file, first
   256 KiB) are sent **obfuscated**. `O = int(md5hex(uk + obfuscated_content_md5 + T)[0:8], 16) mod (size − 262144 +
   1)`. Answers `errno 0` with `info{fs_id, path, …}` on a hit, `errno 404` otherwise. No upload session is needed.
2. **precreate**: `POST /api/precreate?rtype=` form `path, target_path=<dir>/, autoinit=1, block_list=<md5s>,
   local_mtime` (+ `uploadid` to resume) → `uploadid`. (The web app sends a fixed fake block list; real md5s work.)
3. **upload host**: `GET https://d.pcs.baidu.com/rest/2.0/pcs/file?method=locateupload` → `server[]` hostnames.
4. **blocks**: `POST https://<host>/rest/2.0/pcs/superfile2?method=upload&app_id=250528&channel=chunlei&web=1&clienttype=0&path=&uploadid=&uploadsign=0&partseq=i`,
   multipart part `file` (filename `blob`) with the block → `{md5}`. Blocks are 4 MiB (16 MiB from 8 GiB, 64 MiB from
   32 GiB), sent in parallel. **Create the target directory first**: files that create the same new parent at once
   make Baidu fail one of them (-8).
5. **create**: `POST /api/create?isdir=0&rtype=` form `path, size, uploadid, block_list=<md5s from step 4 in order>,
   target_path=<dir>/, local_mtime`.

`rtype`: 0 fails on a clash (-8), 3 overwrites, 1 (the web app's) renames the new file. Zero-byte files upload
normally.

## Download

`GET /api/download?fidlist=[fs_id]&type=dlink&vip=2&sign=S&timestamp=T` with `S = base64(RC4(key=sign3,
data=sign1))` from the template variables → `dlink[0].dlink` (`https://d.pcs.baidu.com/file/…`, valid ~8 h).
Requests to the dlink (browser UA, `Referer: https://pan.baidu.com/`, cookies) redirect once to a
`*.baidupcs.com` file server; ranged requests answer 206 with `Content-Range` and `Content-MD5`.

## Shares

| operation | request | notes |
|---|---|---|
| create | `POST /share/pset?channel=chunlei` form `fid_list=[…], period=<days, 0 = forever>, pwd=<4 chars>, schannel=4, channel_list=[], public=0, is_knowledge=0, eflag_disable=true, linkOrQrcode=link` | → `shareid, link, expiretime` |
| list mine | `GET /share/record?page=&num=100&order=ctime&desc=1&is_batch=1` | `list[]{shareId, shortlink, typicalPath, passwd, lastExpireTime}`; `lastExpireTime` 0 = never (`expiredType` is not what it sounds like: 1 for permanent shares). |
| cancel | `POST /share/cancel?channel=chunlei` form `shareid_list=[…]` | |

**Saving someone's share (转存):**
1. `GET https://pan.baidu.com/s/1<surl>` (browser UA). Without a verified code it redirects to
   `/share/init?surl=<surl>`. The page state is in `locals.mset({…})` (older and mobile pages: `window.locals = {…}`):
   `shareid`, `share_uk`, `bdstoken`, `loginstate`. The netdisk (Android) UA is redirected to `/wap/init` or
   `/wap/error?errortype=0` instead.
2. `POST /share/verify?surl=<surl without the leading 1>&bioc=1&t=<ms>&channel=chunlei` form `pwd, vcode=,
   vcode_str=` → sets the `BDCLND` cookie (`randsk` in the body). A wrong code: -9 or -12.
3. `GET /share/list?shorturl=<surl without 1>&root=1&web=5&page=&num=100&order=time&desc=1&showempty=0&view_mode=1&channel=chunlei`
   → `list[]{fs_id (a string here), server_filename, isdir}`.
4. `POST /share/transfer?shareid=&from=<share_uk>&sekey=<BDCLND, URL-decoded>&async=1&channel=chunlei` form
   `fsidlist=[…], path=<dir>` → `extra.list[]{from, to}`; a big share answers `task_id` and is polled with
   `GET /share/taskquery?taskid=` until `status` is `success` or `failed`. **The target directory must exist.** The
   web app also sends `ondup=newcopy` (a clash makes a copy) and afterwards subscribes to the share
   (`share/subscribe`); bdc does neither, so a repeat fails with errno 4.

While Baidu's share service was down (2026-09-26 afternoon) share pages took ~50 s and then answered 500, or
redirected to `/error/core.html` ("百度网盘正在升级中"), in the browser too.

## Recycle bin

- list: `GET /api/recycle/list/?num=100&page=` → `list[]` with `leftTime` (days).
- restore: `POST /api/recycle/restore?channel=chunlei&async=1` form `fidlist=[…]`.
- permanent delete: `POST /api/recycle/delete?channel=chunlei&async=1` form `fidlist=[…]` → **errno 132** with
  `authwidget` and `verify_scene`: even the web app then asks for an SMS code. Emptying the bin was not tried.

## Offline download (云添加)

`/rest/2.0/services/cloud_dl?method=<m>&t=<ms>` with the common parameters and bdstoken:
- `add_task` (POST form `source_url, save_path, type=3`) → `task_id` (and `rapid_download` 1 when Baidu has the file);
- `list_task` (`need_task_info=1&status=255&start=0&limit=1000`) → `task_info[]`, numbers as strings;
- `query_task` (`op_type=1&task_ids=<id,…>`) → `task_info{<id>: {status, file_size, finished_size, …}}`;
- `cancel_task`, `delete_task` (POST form `task_id`). Status: 0 done, 1 running, 2 system error, 3 not found, 4
  timeout, 5 failed, 6 no space, 7 cancelled.
