# Tasks — docs/SDD-SONAR-S3776-TERNARIES.md

Kind: refactor. Target branch: `feature/sonar-s3776-ternaries` (never `main`).
Gate after extracts: `gocognit -over 15` on `backend/internal` excluding `*_test.go` is empty.
Gate after flatten: no nested `? : ?` in `web/src` production. No NOSONAR. No new libraries.


## Worktree 1 — auth (11)

- [x] **T-001** Characterize auth complexity functions before extract  | tests: 7 | red-green: yes | immutability: yes | wt: 1 | DONE
  - RED: backend/internal/auth/s3776_charter_test.go — table of success+error paths that already hold today. Must pass on current code.
- [x] **T-002** Extract CompleteEmailFactorEnrollment + ConfirmEmailFactor to ≤15  | tests: 3 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: backend/internal/auth/gocognit_test.go CompleteEmailFactorEnrollment (25) and ConfirmEmailFactor (17) ≤15.
- [x] **T-003** Extract tryStepUpProof + ConfirmTOTP to ≤15  | tests: 2 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go tryStepUpProof (23) and ConfirmTOTP (22) ≤15.
- [x] **T-004** Extract CompleteTOTPEnrollment + CreateChallenge* to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go CompleteTOTPEnrollment (22), CreateChallengeEmailOTP (19), CreateChallenge (17) ≤15.
- [x] **T-005** Extract UpdateUser + SetPassword + acceptInvite to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go UpdateUser (23), SetPassword (19), acceptInvite (17) ≤15.
- [x] **T-006** Extract rotateOnce + handleConsumed to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go rotateOnce (17) and handleConsumed (20) ≤15.
- [x] **T-007** Extract AuditStatsSince + ExportAudit to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go AuditStatsSince (23) and ExportAudit (17) ≤15.
- [x] **T-008** Extract ConvertToProvider to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go ConvertToProvider (18) ≤15.
- [x] **T-009** Extract ConsumeEmailChange to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go ConsumeEmailChange (18) ≤15.
- [x] **T-010** Extract AdminHandler.UpdateUser to ≤15  | tests: 3 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go AdminHandler.UpdateUser (18) ≤15.
- [x] **T-011** Extract Handler.Login to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 1 | DONE
  - RED: gocognit_test.go Login (16) ≤15.

## Worktree 2 — backup + folders (12)

- [x] **T-101** Characterize backup/folders complexity functions before extract  | tests: 12 | red-green: yes | immutability: yes | wt: 2 | DONE
  - RED: s3776_charter_test.go in backup/backupagent/backupjobs/backupstatus/folders. Must pass on current code.
- [x] **T-102** Extract folders List, Update, deleteCascade, UpdateInput.Validate to ≤15  | tests: 6 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: folders/gocognit_test.go List(23)/Update(21)/deleteCascade(19)/Validate(16) ≤15.
- [x] **T-103** Extract ValidateJobConfig to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backupjobs/gocognit_test.go ValidateJobConfig (22) ≤15.
- [x] **T-104** Extract backupstatus Handler.Download to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backupstatus/gocognit_test.go Download (18) ≤15.
- [x] **T-105** Extract Service.Export + inspectArchive to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backup/gocognit_test.go Export (22) and inspectArchive (20) ≤15.
- [x] **T-106** Extract loadRestoreLedger + validateManifestIntegrity to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backup/gocognit_test.go loadRestoreLedger (22) and validateManifestIntegrity (16) ≤15.
- [x] **T-107** Extract restoreDuplicateStaged + copyRestoreStaging to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backup/gocognit_test.go restoreDuplicateStaged (16) and copyRestoreStaging (19) ≤15.
- [x] **T-108** Extract spoolNoteMedia + attachPolymorphicTags + copyPolymorphicClicks to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backup/gocognit_test.go spoolNoteMedia (16), attachPolymorphicTags (19), copyPolymorphicClicks (19) ≤15.
- [x] **T-109** Extract DrillJob.Run + DumpJob.Run + UserZipJob.Run to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backupagent/gocognit_test.go DrillJob.Run (24), DumpJob.Run (21), UserZipJob.Run (21) ≤15.
- [x] **T-110** Extract MirrorJob.copyDelta to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backupagent/gocognit_test.go copyDelta (20) ≤15.
- [x] **T-111** Extract scheduleLoop + execute + computeTimings to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backupagent/gocognit_test.go scheduleLoop (21), execute (19), computeTimings (17) ≤15.
- [x] **T-112** Extract GFSPolicy.keep to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 2 | DONE
  - RED: backupagent/gocognit_test.go GFSPolicy.keep (18) ≤15.

## Worktree 3 — remaining Go + web ternaries (20)

- [x] **T-201** Characterize remaining Go complexity functions before extract  | tests: 21 | red-green: yes | immutability: yes | wt: 3 | DONE
  - RED: s3776_charter_test.go in screenshot, preview, mailer, changecheck, mailoutbox, importer, push, pkg/keyfile, tags, roleperm, policy, links, depstatus. Must pass on current code.
- [x] **T-202** Characterize production nested-ternary copy/state before flatten  | tests: 20 | red-green: yes | immutability: yes | wt: 3 | DONE
  - RED: web/src/s3358.charter.test.tsx — one assertion per arm, passing today.
- [x] **T-203** Extract acquireBrowser + Pool.Close to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: screenshot/gocognit_test.go acquireBrowser (24) and Close (16) ≤15.
- [x] **T-204** Extract preview Worker.process + Fetch + parseHead to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: preview/gocognit_test.go process (24), Fetch (16), parseHead (16) ≤15.
- [x] **T-205** Extract smtpMailer.Send + loadAssets + assets.render to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: mailer/gocognit_test.go Send (23), loadAssets (22), render (17) ≤15.
- [x] **T-206** Extract extractMainContent + extractFeedURL + pushLoop to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: changecheck/gocognit_test.go extractMainContent (21), extractFeedURL (19), pushLoop (16) ≤15.
- [x] **T-207** Extract Relay.drain + Ping to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: mailoutbox/gocognit_test.go drain (19) and Ping (16) ≤15.
- [x] **T-208** Extract JSONFile.validateLinks + Handler.parseUpload to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: importer/gocognit_test.go validateLinks (19) and parseUpload (16) ≤15.
- [x] **T-209** Extract push.LoadOrGenerate to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: push/gocognit_test.go LoadOrGenerate (18) ≤15.
- [x] **T-210** Extract keyfile.Load to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: pkg/keyfile/gocognit_test.go Load (18) ≤15.
- [x] **T-211** Extract SetEntityTagsWithPending to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: tags/gocognit_test.go SetEntityTagsWithPending (17) ≤15.
- [x] **T-212** Extract roleperm.Resolve to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: roleperm/gocognit_test.go Resolve (17) ≤15.
- [x] **T-213** Extract Policy.Validate to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: policy/gocognit_test.go Validate (16) ≤15.
- [x] **T-214** Extract ScreenshotHandler.CaptureAndStore to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: links/gocognit_test.go CaptureAndStore (16) ≤15.
- [x] **T-215** Extract Checker.refresh to ≤15  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: depstatus/gocognit_test.go refresh (16) ≤15.
- [x] **T-216** Flatten account nested ternaries  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: web/src/s3358.contracts.test.ts nested-ternary regex on those files must fail today and pass after flatten.
- [x] **T-217** Flatten folder nested ternaries  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: s3358.contracts.test.ts on FolderDialog/FolderPicker/FolderCard.
- [x] **T-218** Flatten backup nested ternaries  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: s3358.contracts.test.ts on those backup UI files.
- [x] **T-219** Flatten audit + stats nested ternaries  | tests: 1 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: s3358.contracts.test.ts on AuditSignals and StatsPage.
- [x] **T-220** Flatten remaining dialog + SW nested ternaries  | tests: 2 | red-green: yes | immutability: no | wt: 3 | DONE
  - RED: s3358.contracts.test.ts on LinkDialog/ImportPreviewDialog/sw.ts and a production-wide empty nested-ternary scan.
