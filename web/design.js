/* design v1.6.0
   dsToast(message, kind)           kind is ok | warn | err | info
   dsError(message, title)          popup error (modal), Esc or button closes it
   dsConfirm(message, opts)         popup question; resolves true for OK, false for Cancel or Esc.
                                    opts = { title, ok, cancel, danger }; danger makes OK a red button
   dsPrompt(message, opts)          popup with one input; resolves the text for OK, null for Cancel or Esc.
                                    opts = { title, ok, cancel, value, type, placeholder }
   dsTabs(root)                    wires .tab[data-tab] in root to the [data-panel] elements with the
                                    same name: .on on the tab, hidden on every other panel
   dsRowClick(tbody, handler)       calls handler(tr) when a tr.clickable is clicked; clicks on links,
                                    buttons and inputs inside the row are ignored
   dsFieldError(el, message, where) inline error next to el; where = "after" (default) | "before".
                                    Call with an empty message to clear it. */
function dsToast(message, kind) {
  var area = document.querySelector(".toast-area");
  if (!area) {
    area = document.createElement("div");
    area.className = "toast-area";
    document.body.appendChild(area);
  }
  var t = document.createElement("div");
  t.className = "toast " + (kind || "info");
  t.setAttribute("role", "status");
  t.textContent = message;
  area.appendChild(t);
  setTimeout(function () { t.remove(); }, 4000);
}

function dsError(message, title) {
  var d = document.getElementById("ds-error-dialog");
  if (!d) {
    d = document.createElement("dialog");
    d.id = "ds-error-dialog";
    d.className = "modal err";
    d.setAttribute("role", "alertdialog");
    d.innerHTML = '<h2></h2><p class="ds-error-msg"></p>' +
      '<form method="dialog"><button class="btn primary">OK</button></form>';
    document.body.appendChild(d);
  }
  d.querySelector("h2").textContent = title || "Something went wrong";
  d.querySelector(".ds-error-msg").textContent = message;
  if (!d.open) d.showModal();
}

function dsConfirm(message, opts) {
  opts = opts || {};
  var d = document.getElementById("ds-confirm-dialog");
  if (!d) {
    d = document.createElement("dialog");
    d.id = "ds-confirm-dialog";
    d.className = "modal wide";
    d.innerHTML = '<h2></h2><p class="ds-confirm-msg"></p>' +
      '<form method="dialog" class="row end">' +
      '<button class="btn ghost" value="cancel"></button>' +
      '<button class="btn primary" value="ok"></button></form>';
    document.body.appendChild(d);
  }
  d.querySelector("h2").textContent = opts.title || "Are you sure?";
  d.querySelector(".ds-confirm-msg").textContent = message;
  var cancel = d.querySelector('[value="cancel"]');
  var ok = d.querySelector('[value="ok"]');
  cancel.textContent = opts.cancel || "Cancel";
  ok.textContent = opts.ok || "OK";
  ok.className = "btn " + (opts.danger ? "danger" : "primary");
  return new Promise(function (resolve) {
    d.onclose = function () {
      var yes = d.returnValue === "ok";
      d.returnValue = "";
      resolve(yes);
    };
    if (!d.open) d.showModal();
    /* A destructive question starts on Cancel, so Enter does not delete by accident. */
    (opts.danger ? cancel : ok).focus();
  });
}

function dsPrompt(message, opts) {
  opts = opts || {};
  var d = document.getElementById("ds-prompt-dialog");
  if (!d) {
    d = document.createElement("dialog");
    d.id = "ds-prompt-dialog";
    d.className = "modal wide";
    d.innerHTML = '<h2></h2><form method="dialog" class="stack tight">' +
      '<label><span class="ds-prompt-msg"></span><input></label>' +
      '<div class="row end"><button class="btn ghost" value="cancel" formnovalidate></button>' +
      '<button class="btn primary" value="ok"></button></div></form>';
    document.body.appendChild(d);
  }
  var input = d.querySelector("input");
  d.querySelector("h2").textContent = opts.title || "Enter a value";
  d.querySelector(".ds-prompt-msg").textContent = message;
  d.querySelector('[value="cancel"]').textContent = opts.cancel || "Cancel";
  d.querySelector('[value="ok"]').textContent = opts.ok || "OK";
  input.type = opts.type || "text";
  input.placeholder = opts.placeholder || "";
  input.value = opts.value || "";
  return new Promise(function (resolve) {
    d.onclose = function () {
      var yes = d.returnValue === "ok";
      d.returnValue = "";
      resolve(yes ? input.value : null);
      input.value = "";
    };
    if (!d.open) d.showModal();
    input.focus();
  });
}

function dsTabs(root) {
  root = root || document;
  var tabs = root.querySelectorAll(".tab[data-tab]");
  function show(name) {
    tabs.forEach(function (t) { t.classList.toggle("on", t.dataset.tab === name); });
    root.querySelectorAll("[data-panel]:not(.tab)").forEach(function (p) { p.hidden = p.dataset.panel !== name; });
  }
  tabs.forEach(function (t) {
    t.addEventListener("click", function () { show(t.dataset.tab); });
  });
  var on = root.querySelector(".tab.on[data-tab]") || tabs[0];
  if (on) show(on.dataset.tab);
  return show;
}

function dsFieldError(el, message, where) {
  var key = "dsErr" + (where === "before" ? "B" : "A");
  var old = el[key];
  if (old) { old.remove(); el[key] = null; }
  el.removeAttribute("aria-invalid");
  if (!message) return;
  var e = document.createElement("span");
  e.className = "error";
  e.setAttribute("role", "alert");
  e.textContent = message;
  el.setAttribute("aria-invalid", "true");
  el.parentNode.insertBefore(e, where === "before" ? el : el.nextSibling);
  el[key] = e;
}

function dsRowClick(tbody, handler) {
  tbody.addEventListener("click", function (e) {
    if (e.target.closest("a, button, input, select, textarea, label")) return;
    var tr = e.target.closest("tr.clickable");
    if (tr && tbody.contains(tr)) handler(tr);
  });
}
