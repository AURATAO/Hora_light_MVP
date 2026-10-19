// HO:RA one-time booking page (server/channel_setup.go). Plain JS, no build.
//
// The person arrives here after replying YES to a priced task in Messages.
// Name and Terms were handled in the chat. Flow: check link → email → code
// (Supabase OTP) → link number (POST /channel/setup/link) → card (SetupIntent
// + Stripe.js, skipped when payments are unavailable) → book
// (POST /channel/setup/finish) → done. Messages gets the confirmation.
//
// Every backend call carries the Supabase access token as a Bearer token;
// dualAuth maps it to the internal user (S-04/S-11). No session cookie needed.
(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };
  var views = ["v-loading", "v-expired", "v-email", "v-code", "v-card", "v-done"];
  var state = { token: "", email: "", accessToken: "", cfg: null, sb: null, stripe: null, card: null, clientSecret: "" };

  function show(view, step) {
    views.forEach(function (v) { $(v).hidden = v !== view; });
    for (var i = 1; i <= 3; i++) $("s" + i).className = step && i <= step ? "on" : "";
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
        showError("Booking is temporarily unavailable. Please try again in a few minutes.");
        $("b-email").disabled = true;
        return;
      }
      state.cfg = cfg.data;
      state.sb = window.supabase.createClient(cfg.data.supabase_url, cfg.data.supabase_publishable_key, {
        auth: { persistSession: false, autoRefreshToken: false, detectSessionInUrl: false },
      });
      $("phone-hint").textContent = chk.data.phone_hint || "your phone";
      if (chk.data.booking) {
        $("booking-title").textContent = chk.data.booking.title;
        $("booking-estimate").textContent = "Estimate " + chk.data.booking.estimate + " · you pay for the time actually used";
        $("booking").hidden = false;
      }
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

  // ── 3. Code → link the number ──────────────────────────────────────────
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
      return api("/channel/setup/link", { method: "POST", body: { token: state.token } }).then(function (l) {
        busy(btn, false);
        if (l.status === 410) { show("v-expired"); return; }
        if (l.status === 409) {
          showError("This phone number is already connected to a different HO:RA account. Use that account's email, or contact info@my-hora.com.");
          return;
        }
        if (!l.ok) { showError("We couldn't connect your number. Please try again."); return; }
        try { sessionStorage.removeItem("hora_setup_t"); } catch (e) { /* ignore */ }
        startCard();
      });
    });
  });
  $("b-back").addEventListener("click", function () { $("code").value = ""; show("v-email", 1); });

  // ── 4. Card ────────────────────────────────────────────────────────────
  function startCard() {
    api("/payments/setup-intent", { method: "POST" }).then(function (r) {
      if (!r.ok || !r.data.client_secret || !r.data.publishable_key) {
        // 503 = payments not configured here (staging). Book straight away;
        // if the server then needs a card, finish() reports it.
        book();
        return;
      }
      state.clientSecret = r.data.client_secret;
      loadStripe(function (ok) {
        if (!ok) { showError("We couldn't load the card form. Check your connection and reload."); return; }
        state.stripe = window.Stripe(r.data.publishable_key);
        state.card = state.stripe.elements().create("card", {
          style: { base: { fontSize: "16px", color: "#222831", fontFamily: "Inter, system-ui, sans-serif" } },
        });
        state.card.mount("#card-element");
        show("v-card", 3);
      });
    }).catch(function () { book(); });
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
      payment_method: { card: state.card, billing_details: { email: state.email } },
    }).then(function (res) {
      if (res.error) { busy(btn, false); showError(res.error.message || "Your card couldn't be saved. Try another card."); return; }
      book(btn);
    });
  });
  $("b-skip").addEventListener("click", function () { book(); });

  // ── 5. Book ────────────────────────────────────────────────────────────
  function book(btn) {
    if (btn) busy(btn, true, "Booking…");
    api("/channel/setup/finish", { method: "POST" }).then(function (r) {
      if (btn) busy(btn, false);
      var outcome = (r.data && r.data.outcome) || "";
      if (outcome === "booked") {
        done("Booked", "Go back to Messages. We'll text you when a supporter accepts.", "");
      } else if (outcome === "needs_card") {
        done("Almost there", "We need a card to book. Check Messages for a link to add one.", "");
      } else if (outcome === "declined" || outcome === "outstanding_balance" || outcome === "needs_authentication") {
        done("Almost there", "We couldn't place the booking yet. Check Messages for what to do next.", "");
      } else {
        done("You're all set", "Go back to Messages and tell HO:RA what you need.", "");
      }
    }).catch(function () {
      if (btn) busy(btn, false);
      showError("We couldn't finish booking. Please try again.");
    });
  }

  function done(title, lead, note) {
    $("done-title").textContent = title;
    $("done-lead").textContent = lead;
    if (note) { $("done-note").textContent = note; $("done-note").hidden = false; }
    $("done-mark").textContent = title === "Booked" ? "✓" : "→";
    show("v-done", 3);
  }

  function otpMessage(err) {
    var m = (err && err.message) || "";
    if (/rate|seconds|too many/i.test(m)) return "Too many codes requested. Wait a minute, then try again.";
    return "We couldn't send the code. Check the email address and try again.";
  }

  start();
})();
