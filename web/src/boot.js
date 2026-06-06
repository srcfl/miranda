// web/src/boot.js — external bootstrap so the page needs no inline module script
// (keeps CSP script-src tight: 'self' + a per-request nonce only on the import map).
import { start } from './app.js';
start(document.getElementById('app'));
