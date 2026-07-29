const $ = (selector) => document.querySelector(selector);
let endpoints = [];
let selected;
let events = [];
let source;

async function request(path, options) {
  const response = await fetch(path, options);
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || "Request failed");
  return data;
}

async function loadEndpoints() {
  endpoints = await request("/api/endpoints");
  renderEndpoints();
  if (endpoints.length) selectEndpoint(endpoints[0]);
}

$("#create-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const endpoint = await request("/api/endpoints", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({name: $("#endpoint-name").value})
    });
    endpoints.unshift(endpoint);
    $("#endpoint-name").value = "";
    $("#workspace").classList.remove("hidden");
    renderEndpoints();
    selectEndpoint(endpoint);
  } catch (error) { alert(error.message); }
});

$("#new-endpoint").addEventListener("click", () => {
  $("#endpoint-name").focus();
  window.scrollTo({top: 0, behavior: "smooth"});
});

function renderEndpoints() {
  $("#workspace").classList.toggle("hidden", endpoints.length === 0);
  $("#endpoint-list").innerHTML = endpoints.map(endpoint => `
    <div class="endpoint-item ${selected?.token === endpoint.token ? "active" : ""}" data-token="${endpoint.token}">
      <strong>${escapeHTML(endpoint.name)}</strong><small>${endpoint.token.slice(0, 10)}…</small>
    </div>`).join("");
  document.querySelectorAll(".endpoint-item").forEach(element =>
    element.addEventListener("click", () => selectEndpoint(endpoints.find(e => e.token === element.dataset.token))));
}

async function selectEndpoint(endpoint) {
  selected = endpoint;
  renderEndpoints();
  $("#hook-url").textContent = `${location.origin}/hooks/${endpoint.token}`;
  events = await request(`/api/endpoints/${endpoint.token}/events`);
  renderEvents();
  $("#detail").className = "detail empty";
  $("#detail").innerHTML = `<div class="empty-icon">↙</div><h3>Select a request</h3><p>Method, headers, query parameters and body will appear here.</p>`;
  if (source) source.close();
  source = new EventSource(`/api/endpoints/${endpoint.token}/stream`);
  source.addEventListener("webhook", message => {
    events.unshift(JSON.parse(message.data));
    renderEvents();
  });
}

function renderEvents() {
  const list = $("#event-list");
  if (!events.length) {
    list.className = "empty";
    list.textContent = "Waiting for the first webhook…";
    return;
  }
  list.className = "";
  list.innerHTML = events.map(item => `
    <div class="event" data-id="${item.id}">
      <span class="method">${escapeHTML(item.method)}</span>
      <div><strong>${escapeHTML(item.path)}</strong><time>${new Date(item.createdAt).toLocaleString()}</time></div>
    </div>`).join("");
  document.querySelectorAll(".event").forEach(element =>
    element.addEventListener("click", () => showEvent(events.find(e => String(e.id) === element.dataset.id))));
}

function showEvent(event) {
  document.querySelectorAll(".event").forEach(e => e.classList.toggle("active", e.dataset.id === String(event.id)));
  const prettyBody = formatBody(event.body);
  $("#detail").className = "detail";
  $("#detail").innerHTML = `
    <div class="detail-head"><span class="method">${escapeHTML(event.method)}</span><strong>${escapeHTML(event.path)}</strong></div>
    <label>BODY</label><pre>${escapeHTML(prettyBody || "(empty)")}</pre>
    <label>QUERY STRING</label><pre>${escapeHTML(event.query || "(empty)")}</pre>
    <label>HEADERS</label><pre>${escapeHTML(JSON.stringify(event.headers, null, 2))}</pre>
    <form class="replay" id="replay-form">
      <input type="url" id="replay-url" placeholder="https://your-app.com/webhook" required>
      <button>Replay</button>
    </form>`;
  $("#replay-form").addEventListener("submit", async submit => {
    submit.preventDefault();
    try {
      const result = await request(`/api/events/${event.id}/replay`, {
        method: "POST", headers: {"Content-Type": "application/json"},
        body: JSON.stringify({url: $("#replay-url").value})
      });
      alert(`Replay completed with HTTP ${result.status}`);
    } catch (error) { alert(error.message); }
  });
}

$("#copy-url").addEventListener("click", async () => {
  await navigator.clipboard.writeText($("#hook-url").textContent);
  $("#copy-url").textContent = "Copied!";
  setTimeout(() => $("#copy-url").textContent = "Copy URL", 1200);
});

function formatBody(body) {
  try { return JSON.stringify(JSON.parse(body), null, 2); } catch { return body; }
}
function escapeHTML(value = "") {
  return String(value).replace(/[&<>"']/g, char => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#039;"}[char]));
}

loadEndpoints().catch(console.error);
