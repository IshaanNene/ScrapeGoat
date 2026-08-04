package dashboard

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ScrapeGoat Dashboard</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body { font-family: 'Inter', -apple-system, system-ui, sans-serif; background: #0f172a; color: #e2e8f0; min-height: 100vh; }
        .header { background: linear-gradient(135deg, #1e293b, #334155); padding: 1.5rem 2rem; border-bottom: 1px solid #475569; display: flex; justify-content: space-between; align-items: center; }
        .header h1 { font-size: 1.5rem; background: linear-gradient(135deg, #38bdf8, #818cf8); background-clip: text; -webkit-background-clip: text; -webkit-text-fill-color: transparent; }
        .header .status { padding: 0.5rem 1rem; border-radius: 9999px; font-size: 0.875rem; font-weight: 600; }
        .status.running { background: #166534; color: #4ade80; }
        .status.stopped { background: #991b1b; color: #fca5a5; }
        .status.idle { background: #854d0e; color: #fde047; }
        .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(240px, 1fr)); gap: 1rem; padding: 2rem; }
        .card { background: #1e293b; border: 1px solid #334155; border-radius: 12px; padding: 1.5rem; transition: transform 0.2s; }
        .card:hover { transform: translateY(-2px); }
        .card .label { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.05em; color: #94a3b8; margin-bottom: 0.5rem; }
        .card .value { font-size: 2rem; font-weight: 700; color: #f1f5f9; }
        .card .sub { font-size: 0.875rem; color: #64748b; margin-top: 0.25rem; }
        .card.accent { border-color: #38bdf8; }
        .card.accent .value { color: #38bdf8; }
        .card.success { border-color: #4ade80; }
        .card.success .value { color: #4ade80; }
        .card.warning { border-color: #fbbf24; }
        .card.warning .value { color: #fbbf24; }
        .card.error { border-color: #f87171; }
        .card.error .value { color: #f87171; }
        .footer { text-align: center; padding: 1rem; color: #475569; font-size: 0.75rem; }
    </style>
</head>
<body>
    <div class="header">
        <h1>ScrapeGoat Dashboard</h1>
        <span class="status idle" id="status">Idle</span>
    </div>
    <div class="grid" id="stats">
        <div class="card accent"><div class="label">Requests Sent</div><div class="value" id="requests_sent">0</div></div>
        <div class="card error"><div class="label">Requests Failed</div><div class="value" id="requests_failed">0</div></div>
        <div class="card success"><div class="label">Responses OK</div><div class="value" id="responses_ok">0</div></div>
        <div class="card success"><div class="label">Items Scraped</div><div class="value" id="items_scraped">0</div></div>
        <div class="card warning"><div class="label">Items Dropped</div><div class="value" id="items_dropped">0</div></div>
        <div class="card accent"><div class="label">URLs Enqueued</div><div class="value" id="urls_enqueued">0</div></div>
        <div class="card"><div class="label">URLs Filtered</div><div class="value" id="urls_filtered">0</div></div>
        <div class="card"><div class="label">Bytes Downloaded</div><div class="value" id="bytes_downloaded">0</div><div class="sub" id="bytes_human"></div></div>
        <div class="card accent"><div class="label">Active Workers</div><div class="value" id="active_workers">0</div></div>
        <div class="card"><div class="label">Elapsed</div><div class="value" id="elapsed">0s</div></div>
    </div>
    <div class="footer">ScrapeGoat v1.0 — Auto-refreshes every 2s</div>
    <script>
        async function refresh() {
            try {
                const r = await fetch('/api/stats');
                const d = await r.json();
                document.getElementById('status').textContent = d.state || 'unknown';
                document.getElementById('status').className = 'status ' + (d.state || 'idle');
                ['requests_sent','requests_failed','responses_ok','items_scraped','items_dropped','urls_enqueued','urls_filtered','active_workers'].forEach(k => {
                    const el = document.getElementById(k);
                    if (el && d[k] !== undefined) el.textContent = Number(d[k]).toLocaleString();
                });
                const b = document.getElementById('bytes_downloaded');
                if (b && d.bytes_downloaded) { b.textContent = Number(d.bytes_downloaded).toLocaleString(); document.getElementById('bytes_human').textContent = humanize(d.bytes_downloaded); }
                const e = document.getElementById('elapsed');
                if (e && d.elapsed) e.textContent = d.elapsed;
            } catch(e) {}
        }
        function humanize(b) { const u=['B','KB','MB','GB']; let i=0; while(b>=1024&&i<u.length-1){b/=1024;i++;} return b.toFixed(1)+' '+u[i]; }
        setInterval(refresh, 2000);
        refresh();
    </script>
</body>
</html>`
