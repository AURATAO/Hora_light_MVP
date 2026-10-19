// HO:RA one-page setup (server/channel_setup.go). Plain JS, no build step.
//
// Flow: check link → email → code (Supabase OTP) → name + terms (PATCH /profile)
// → link number (POST /channel/setup/link) → card (SetupIntent + Stripe.js,
// skipped when payments are unavailable) → done.
//
// Every backend call carries the Supabase access token as a Bearer token;
// dualAuth maps it to the internal user (S-04/S-11). No session cookie needed.
(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };
  var views = ["v-loading", "v-expired", "v-email", "v-code", "v-profile", "v-card", "v-done"];
  var state = { token: "", email: "", accessToken: "", cfg: null, sb: null, stripe: null, card: null, clientSecret: "" };

  function show(view, step) {
    views.forEach(function (v) { $(v).hidden = v !== view; });
    for (var i = 1; i <= 4; i++) $("s" + i).className = step && i <= step ? "on" : "";
    clearError();
    var first = $(view).querySelector("input");
    if (first) setTimeout(function () { first.focus(); }, 50);
  }
  function showError(msg) { var e = $("err"); e.textContent = msg; e.className = "err show"; }
  function clearError() { $("err").className = "err"; $("err").textContent = ""; }
  function busy(btn, on, label) {
    btn.disabled = on;
    if (on) { btn.dataset.label = btn.textContent; btn.textContent = label || "One moment…"; }
    else if (btn.dataset.label) { btn.textContent = btn.dataset.label; }
  }

  function api(path, opts) {
    opts = opts || {};
    var headers = { "Content-Type": "application/json" };
    if (state.accessToken) headers.Authorization = "Bearer " + state.accessToken;
    return fetch(path, {
      method: opts.method || "GET",
      headers: headers,
      body: opts.body ? JSON.stringify(opts.body) : undefined,
      credentials: "same-origin",
    }).then(function (res) {
      return res.text().then(function (t) {
        var data = null;
        try { data = t ? JSON.parse(t) : null; } catch (e) { data = null; }
        return { ok: res.ok, status: res.status, data: data || {} };
      });
    });
  }

  // ── 1. Link ────────────────────────────────────────────────────────────
  function readToken() {
    var t = new URLSearchParams(location.search).get("t") || "";
    try {
      if (t) sessionStorage.setItem("hora_setup_t", t);
      else t = sessionStorage.getItem("hora_setup_t") || "";
    } catch (e) { /* storage blocked: keep it in memory only */ }
    // Take the token out of the address bar (screenshots, history, sharing).
    if (location.search) history.replaceState(null, "", location.pathname);
    return t;
  }

  function start() {
    state.token = readToken();
    if (!state.token) { show("v-expired"); return; }
    Promise.all([
      api("/channel/setup/config"),
      api("/channel/setup/check", { method: "POST", body: { token: state.token } }),
    ]).then(function (r) {
      var cfg = r[0], chk = r[1];
      if (!chk.ok) { show("v-expired"); return; }
      if (!cfg.ok || !cfg.data.supabase_url || !cfg.data.supabase_publishable_key || !window.supabase) {
        show("v-email", 1);
        showError("Setup is temporarily unavailable. Please try again in a few minutes.");
        $("b-email").disabled = true;
        return;
      }
      state.cfg = cfg.data;
      state.sb = window.supabase.createClient(cfg.data.supabase_url, cfg.data.supabase_publishable_key, {
        auth: { persistSession: false, autoRefreshToken: false, detectSessionInUrl: false },
      });
      $("phone-hint").textContent = chk.data.phone_hint || "your phone";
      $("terms-link").href = cfg.data.terms_url;
      $("privacy-link").href = cfg.data.privacy_url;
      show("v-email", 1);
    }).catch(function () {
      show("v-email", 1);
      showError("We couldn't reach HO:RA. Check your connection and reload the page.");
    });
  }

  // ── 2. Email ───────────────────────────────────────────────────────────
  $("f-email").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var email = $("email").value.trim().toLowerCase();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) { showError("Enter a valid email address."); return; }
    clearError();
    var btn = $("b-email"); busy(btn, true, "Sending…");
    state.sb.auth.signInWithOtp({ email: email, options: { shouldCreateUser: true } }).then(function (res) {
      busy(btn, false);
      if (res.error) { showError(otpMessage(res.error)); return; }
      state.email = email;
      $("email-echo").textContent = email;
      show("v-code", 2);
    });
  });

  // ── 3. Code ────────────────────────────────────────────────────────────
  $("f-code").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var code = $("code").value.replace(/\D/g, "");
    if (code.length < 6) { showError("Enter the 6-digit code from the email."); return; }
    clearError();
    var btn = $("b-code"); busy(btn, true, "Checking…");
    state.sb.auth.verifyOtp({ email: state.email, token: code, type: "email" }).then(function (res) {
      if (res.error || !res.data || !res.data.session) {
        busy(btn, false);
        showError("That code didn't work. Check the latest email, or go back and resend.");
        return;
      }
      state.accessToken = res.data.session.access_token;
      // GET creates the profile row if it doesn't exist yet; prefill the name.
      return api("/profile").then(function (p) {
        busy(btn, false);
        if (!p.ok) { showError("We couldn't load your account. Please try again."); return; }
        if (p.data && p.data.name) $("name").value = p.data.name;
        show("v-profile", 3);
      });
    });
  });
  $("b-back").addEventListener("click", function () { $("code").value = ""; show("v-email", 1); });

  // ── 4. Name + terms → link the number ──────────────────────────────────
  $("f-profile").addEventListener("submit", function (ev) {
    ev.preventDefault();
    var name = $("name").value.trim();
    if (!name) { showError("Enter your name."); return; }
    if (!$("terms").checked) { showError("Please agree to the Terms and Privacy Policy to continue."); return; }
    clearError();
    var btn = $("b-profile"); busy(btn, true, "Saving…");
    api("/profile", { method: "PATCH", body: { name: name, terms_accepted: true } }).then(function (p) {
      if (!p.ok) { busy(btn, false); showError("We couldn't save your name. Please try again."); return; }
      return api("/channel/setup/link", { method: "POST", body: { token: state.token } }).then(function (l) {
        busy(btn, false);
        if (l.status === 410) { show("v-expired"); return; }
        if (l.status === 409) {
          showError("This phone number is already connected to a different HO:RA account. Sign in with that account's email, or contact info@my-hora.com.");
          return;
        }
        if (!l.ok) { showError("We couldn't connect your number. Please try again."); return; }
        try { sessionStorage.removeItem("hora_setup_t"); } catch (e) { /* ignore */ }
        startCard();
      });
    });
  });

  // ── 5. Card ────────────────────────────────────────────────────────────
  function startCard() {
    api("/payments/setup-intent", { method: "POST" }).then(function (r) {
      if (!r.ok || !r.data.client_secret || !r.data.publishable_key) {
        // 503 = payments not configured here. Linking is done; the card is
        // asked for again before the first task is posted.
        finish("You can add a card when you post your first task.");
        return;
      }
      state.clientSecret = r.data.client_secret;
      loadStripe(function (ok) {
        if (!ok) { finish("You can add a card when you post your first task."); return; }
        state.stripe = window.Stripe(r.data.publishable_key);
        state.card = state.stripe.elements().create("card", {
          hidePostalCode: false,
          style: { base: { fontSize: "16px", color: "#222831", fontFamily: "Inter, system-ui, sans-serif" } },
        });
        state.card.mount("#card-element");
        show("v-card", 4);
      });
    }).catch(function () { finish("You can add a card when you post your first task."); });
  }

  function loadStripe(cb) {
    if (window.Stripe) { cb(true); return; }
    var s = document.createElement("script");
    s.src = "https://js.stripe.com/v3/";
    s.onload = function () { cb(!!window.Stripe); };
    s.onerror = function () { cb(false); };
    document.head.appendChild(s);
  }

  $("f-card").addEventListener("submit", function (ev) {
    ev.preventDefault();
    clearError();
    var btn = $("b-card"); busy(btn, true, "Saving…");
    state.stripe.confirmCardSetup(state.clientSecret, {
      payment_method: { card: state.card, billing_details: { name: $("name").value.trim(), email: state.email } },
    }).then(function (res) {
      busy(btn, false);
      if (res.error) { showError(res.error.message || "Your card couldn't be saved. Try another card."); return; }
      finish("");
    });
  });
  $("b-skip").addEventListener("click", function () { finish("You can add a card when you post your first task."); });

  function finish(note) {
    if (note) { $("done-note").textContent = note; $("done-note").hidden = false; }
    show("v-done", 4);
  }

  function otpMessage(err) {
    var m = (err && err.message) || "";
    if (/rate|seconds|too many/i.test(m)) return "Too many codes requested. Wait a minute, then try again.";
    return "We couldn't send the code. Check the email address and try again.";
  }

  start();
})();
