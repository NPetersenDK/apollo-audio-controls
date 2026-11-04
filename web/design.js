/* design v1.2.0
   dsToast(message, kind)          kind is ok | warn | err | info
   dsError(message, title)         popup error (modal), Esc or button closes it
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
