// Activity Console Client-Side Controller (ADR 0017)
// ponytail: Uses native browser EventSource and requestAnimationFrame with a strict 1,000 item FIFO memory cap.

let activityLogs = [];
let activityConsoleOpen = false;
let activityPaused = false;
let activityLevelFilter = 'ALL';
let activitySearchQuery = '';
let activityAutoScroll = true;
let activityUnreadErrors = 0;
let activityEventSource = null;
let activityRenderScheduled = false;

function initActivityStream() {
  if (activityEventSource) {
    activityEventSource.close();
  }

  const statusEl = document.getElementById('activity-live-status');
  activityEventSource = new EventSource('/api/telemetry/stream');

  activityEventSource.onopen = () => {
    if (statusEl && !activityPaused) {
      statusEl.textContent = '● LIVE';
      statusEl.className = 'inline-flex items-center px-1.5 py-0.2 rounded text-[10px] font-mono bg-emerald-950/80 text-emerald-400 border border-emerald-700/50';
    }
  };

  activityEventSource.onmessage = (event) => {
    try {
      const evt = JSON.parse(event.data);
      appendActivityEvent(evt);
    } catch (e) {
      console.error('Failed to parse telemetry event', e);
    }
  };

  activityEventSource.onerror = () => {
    if (statusEl && !activityPaused) {
      statusEl.textContent = '⚠️ RECONNECTING';
      statusEl.className = 'inline-flex items-center px-1.5 py-0.2 rounded text-[10px] font-mono bg-amber-950/80 text-amber-400 border border-amber-700/50';
    }
  };
}

function appendActivityEvent(evt) {
  // Enforce memory bounds: cap at 1,000 items in browser FIFO
  activityLogs.push(evt);
  if (activityLogs.length > 1000) {
    activityLogs.shift();
  }

  const lvl = (evt.level || '').toUpperCase();
  if (lvl === 'ERROR' && !activityConsoleOpen) {
    activityUnreadErrors++;
    updateActivityErrorBadge();
  }

  scheduleActivityRender();
}

function scheduleActivityRender() {
  if (activityRenderScheduled || !activityConsoleOpen || activityPaused) return;
  activityRenderScheduled = true;
  requestAnimationFrame(() => {
    activityRenderScheduled = false;
    renderActivityLogs();
  });
}

function updateActivityErrorBadge() {
  const badge = document.getElementById('activity-error-badge');
  if (!badge) return;
  if (activityUnreadErrors > 0) {
    badge.textContent = activityUnreadErrors > 99 ? '99+' : activityUnreadErrors;
    badge.classList.remove('hidden');
  } else {
    badge.classList.add('hidden');
  }
}

function toggleActivityConsole(forceState) {
  const drawer = document.getElementById('activity-console-drawer');
  if (!drawer) return;
  activityConsoleOpen = typeof forceState === 'boolean' ? forceState : drawer.classList.contains('hidden');

  if (activityConsoleOpen) {
    drawer.classList.remove('hidden');
    activityUnreadErrors = 0;
    updateActivityErrorBadge();
    renderActivityLogs();
    if (activityAutoScroll) {
      scrollActivityToBottom();
    }
  } else {
    drawer.classList.add('hidden');
  }
}

function toggleActivityPause() {
  activityPaused = !activityPaused;
  const btnIcon = document.getElementById('activity-pause-icon');
  const btnLabel = document.getElementById('activity-pause-label');
  const statusEl = document.getElementById('activity-live-status');

  if (activityPaused) {
    if (btnIcon) btnIcon.textContent = '▶';
    if (btnLabel) btnLabel.textContent = 'Resume';
    if (statusEl) {
      statusEl.textContent = '⏸ PAUSED';
      statusEl.className = 'inline-flex items-center px-1.5 py-0.2 rounded text-[10px] font-mono bg-amber-950/80 text-amber-400 border border-amber-700/50';
    }
  } else {
    if (btnIcon) btnIcon.textContent = '⏸';
    if (btnLabel) btnLabel.textContent = 'Pause';
    if (statusEl) {
      statusEl.textContent = '● LIVE';
      statusEl.className = 'inline-flex items-center px-1.5 py-0.2 rounded text-[10px] font-mono bg-emerald-950/80 text-emerald-400 border border-emerald-700/50';
    }
    renderActivityLogs();
    if (activityAutoScroll) {
      scrollActivityToBottom();
    }
  }
}

function setActivityLevelFilter(level) {
  activityLevelFilter = level;
  const levels = ['ALL', 'ERROR', 'WARN', 'INFO', 'DEBUG'];
  levels.forEach(lvl => {
    const btn = document.getElementById(`activity-filter-${lvl.toLowerCase()}`);
    if (!btn) return;
    if (lvl === level) {
      btn.className = 'px-2 py-0.5 rounded font-mono transition bg-indigo-600 text-white font-medium';
    } else {
      btn.className = 'px-2 py-0.5 rounded font-mono transition text-zinc-400 hover:text-white';
    }
  });
  renderActivityLogs();
}

function onActivitySearchInput(val) {
  activitySearchQuery = (val || '').toLowerCase().trim();
  renderActivityLogs();
}

function clearActivityLogs() {
  activityLogs = [];
  activityUnreadErrors = 0;
  updateActivityErrorBadge();
  renderActivityLogs();
}

function handleActivityScroll() {
  const container = document.getElementById('activity-table-container');
  if (!container) return;
  // Smart Auto-Scroll: if user scrolled away from bottom, pause auto-scroll
  const isAtBottom = container.scrollHeight - container.scrollTop - container.clientHeight < 30;
  activityAutoScroll = isAtBottom;
}

function scrollActivityToBottom() {
  const container = document.getElementById('activity-table-container');
  if (container) {
    container.scrollTop = container.scrollHeight;
  }
}

function renderActivityLogs() {
  const tbody = document.getElementById('activity-table-body');
  const emptyState = document.getElementById('activity-empty-state');
  if (!tbody) return;

  const filtered = activityLogs.filter(item => {
    // Level filter
    if (activityLevelFilter !== 'ALL') {
      const itemLvl = (item.level || '').toUpperCase();
      if (itemLvl !== activityLevelFilter) {
        if (!(activityLevelFilter === 'WARN' && itemLvl === 'WARNING')) {
          return false;
        }
      }
    }
    // Search query
    if (activitySearchQuery) {
      const msg = (item.message || '').toLowerCase();
      const mod = (item.module || '').toLowerCase();
      const tgt = (item.target || '').toLowerCase();
      const attrsStr = item.attrs ? JSON.stringify(item.attrs).toLowerCase() : '';
      if (!msg.includes(activitySearchQuery) && !mod.includes(activitySearchQuery) && !tgt.includes(activitySearchQuery) && !attrsStr.includes(activitySearchQuery)) {
        return false;
      }
    }
    return true;
  });

  if (filtered.length === 0) {
    tbody.innerHTML = '';
    if (emptyState) emptyState.classList.remove('hidden');
    return;
  }

  if (emptyState) emptyState.classList.add('hidden');

  const rows = filtered.map(item => {
    const timeStr = formatActivityTime(item.timestamp);
    const lvlBadge = getActivityLevelBadge(item.level);
    const modBadge = getActivityModuleBadge(item.module);
    const tgtBadge = getActivityTargetBadge(item.target);
    const metaStr = getActivityMetaString(item);
    const hasDetails = item.attrs && Object.keys(item.attrs).length > 0;
    const rowId = `act-row-${item.id}`;

    let detailRow = '';
    if (hasDetails) {
      detailRow = `
        <tr id="${rowId}-detail" class="hidden bg-zinc-900/60 font-mono text-[10px] text-zinc-400">
          <td colspan="6" class="py-1.5 px-4">
            <div class="p-2 bg-zinc-950 rounded border border-zinc-800 overflow-x-auto whitespace-pre-wrap select-text">${escapeHtml(JSON.stringify(item.attrs, null, 2))}</div>
          </td>
        </tr>
      `;
    }

    return `
      <tr class="hover:bg-zinc-900/50 transition cursor-pointer group" onclick="toggleActivityRowDetail('${rowId}-detail')">
        <td class="py-1 px-3 text-zinc-400 whitespace-nowrap">${escapeHtml(timeStr)}</td>
        <td class="py-1 px-2 whitespace-nowrap">${lvlBadge}</td>
        <td class="py-1 px-2 whitespace-nowrap">${modBadge}</td>
        <td class="py-1 px-2 whitespace-nowrap">${tgtBadge}</td>
        <td class="py-1 px-3 text-zinc-200 group-hover:text-white break-all leading-snug">${escapeHtml(item.message || '')}</td>
        <td class="py-1 px-3 text-right text-zinc-400 whitespace-nowrap">${metaStr} ${hasDetails ? '<span class="text-[9px] text-zinc-500">▶</span>' : ''}</td>
      </tr>
      ${detailRow}
    `;
  }).join('');

  tbody.innerHTML = rows;

  if (activityAutoScroll) {
    scrollActivityToBottom();
  }
}

function toggleActivityRowDetail(id) {
  const el = document.getElementById(id);
  if (el) {
    el.classList.toggle('hidden');
  }
}

function formatActivityTime(ts) {
  if (!ts) return '--:--:--.---';
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  const pad = (n, s=2) => String(n).padStart(s, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

function getActivityLevelBadge(lvl) {
  const l = (lvl || 'INFO').toUpperCase();
  switch (l) {
    case 'ERROR':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-bold bg-rose-950/80 text-rose-400 border border-rose-800/50">ERR</span>';
    case 'WARN':
    case 'WARNING':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-bold bg-amber-950/80 text-amber-400 border border-amber-800/50">WARN</span>';
    case 'DEBUG':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-zinc-800 text-zinc-400 border border-zinc-700/50">DBG</span>';
    default:
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-blue-950/80 text-blue-400 border border-blue-800/50">INFO</span>';
  }
}

function getActivityModuleBadge(mod) {
  const m = (mod || 'app').toLowerCase();
  switch (m) {
    case 'ssh':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-violet-950/60 text-violet-300 border border-violet-800/40">ssh</span>';
    case 'poller':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-emerald-950/60 text-emerald-300 border border-emerald-800/40">poller</span>';
    case 'deploy':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-indigo-950/60 text-indigo-300 border border-indigo-800/40">deploy</span>';
    case 'gitsync':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-cyan-950/60 text-cyan-300 border border-cyan-800/40">git</span>';
    case 'notifier':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-pink-950/60 text-pink-300 border border-pink-800/40">notify</span>';
    case 'auth':
      return '<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-amber-950/60 text-amber-300 border border-amber-800/40">auth</span>';
    default:
      return `<span class="px-1.5 py-0.2 rounded text-[10px] font-medium bg-zinc-800 text-zinc-300 border border-zinc-700/50">${escapeHtml(m)}</span>`;
  }
}

function getActivityTargetBadge(tgt) {
  if (!tgt || tgt === '-') {
    return '<span class="text-zinc-600">-</span>';
  }
  return `<span class="px-1.5 py-0.2 rounded text-[10px] font-mono bg-zinc-800/80 text-zinc-300 border border-zinc-700/50">${escapeHtml(tgt)}</span>`;
}

function getActivityMetaString(item) {
  if (!item.attrs) return '';
  const parts = [];
  if (item.attrs.duration_ms !== undefined) parts.push(`${item.attrs.duration_ms}ms`);
  if (item.attrs.exit_code !== undefined) parts.push(`exit:${item.attrs.exit_code}`);
  if (item.attrs.status !== undefined) parts.push(`st:${item.attrs.status}`);
  if (item.attrs.version !== undefined) parts.push(`${item.attrs.version}`);
  return parts.join(' • ');
}

function toggleActivityExportMenu() {
  const menu = document.getElementById('activity-export-menu');
  if (menu) {
    menu.classList.toggle('hidden');
  }
}

function exportActivityLogs(format) {
  toggleActivityExportMenu();
  let content = '';
  const tsStamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-');
  let filename = `docker-orkes-activity-${tsStamp}`;

  if (format === 'json') {
    content = JSON.stringify(activityLogs, null, 2);
    filename += '.json';
  } else {
    filename += '.log';
    content = activityLogs.map(item => {
      const ts = item.timestamp || '';
      const lvl = (item.level || 'INFO').padEnd(5);
      const mod = (item.module || 'controlplane').padEnd(10);
      const tgt = (item.target || '-').padEnd(14);
      const msg = item.message || '';
      const meta = item.attrs && Object.keys(item.attrs).length ? ' ' + JSON.stringify(item.attrs) : '';
      return `${ts} [${lvl}] [${mod}] [${tgt}] ${msg}${meta}`;
    }).join('\n');
  }

  const blob = new Blob([content], { type: format === 'json' ? 'application/json' : 'text/plain;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

function escapeHtml(str) {
  if (!str) return '';
  return String(str)
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

// Global hotkey: Ctrl + ` (backquote)
document.addEventListener('keydown', (e) => {
  if (e.ctrlKey && e.key === '`') {
    e.preventDefault();
    toggleActivityConsole();
  }
});

// Close export menu on outside click
document.addEventListener('click', (e) => {
  const menu = document.getElementById('activity-export-menu');
  if (menu && !menu.classList.contains('hidden')) {
    if (!e.target.closest('#activity-export-menu') && !e.target.closest('button[onclick*="toggleActivityExportMenu"]')) {
      menu.classList.add('hidden');
    }
  }
});

// Auto-initialize when DOM is ready
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', initActivityStream);
} else {
  initActivityStream();
}
