/**
* @vue/shared v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
// @__NO_SIDE_EFFECTS__
function Dt(e) {
  const t = /* @__PURE__ */ Object.create(null);
  for (const n of e.split(",")) t[n] = 1;
  return (n) => n in t;
}
const Ne = process.env.NODE_ENV !== "production" ? Object.freeze({}) : {};
process.env.NODE_ENV !== "production" && Object.freeze([]);
const Ye = () => {
}, Rt = (e) => e.charCodeAt(0) === 111 && e.charCodeAt(1) === 110 && // uppercase letter
(e.charCodeAt(2) > 122 || e.charCodeAt(2) < 97), Tt = (e) => e.startsWith("onUpdate:"), P = Object.assign, Vt = Object.prototype.hasOwnProperty, we = (e, t) => Vt.call(e, t), g = Array.isArray, Y = (e) => Je(e) === "[object Map]", b = (e) => typeof e == "function", x = (e) => typeof e == "string", te = (e) => typeof e == "symbol", v = (e) => e !== null && typeof e == "object", It = (e) => (v(e) || b(e)) && b(e.then) && b(e.catch), Ct = Object.prototype.toString, Je = (e) => Ct.call(e), qe = (e) => Je(e).slice(8, -1), Ie = (e) => x(e) && e !== "NaN" && e[0] !== "-" && "" + parseInt(e, 10) === e, Ge = (e) => {
  const t = /* @__PURE__ */ Object.create(null);
  return ((n) => t[n] || (t[n] = e(n)));
}, Qe = Ge((e) => e.charAt(0).toUpperCase() + e.slice(1)), At = Ge(
  (e) => e ? `on${Qe(e)}` : ""
), A = (e, t) => !Object.is(e, t);
let We;
const he = () => We || (We = typeof globalThis < "u" ? globalThis : typeof self < "u" ? self : typeof window < "u" ? window : typeof global < "u" ? global : {});
function Ce(e) {
  if (g(e)) {
    const t = {};
    for (let n = 0; n < e.length; n++) {
      const r = e[n], s = x(r) ? Ft(r) : Ce(r);
      if (s)
        for (const o in s)
          t[o] = s[o];
    }
    return t;
  } else if (x(e) || v(e))
    return e;
}
const $t = /;(?![^(]*\))/g, Mt = /:([^]+)/, Pt = /\/\*[^]*?\*\//g;
function Ft(e) {
  const t = {};
  return e.replace(Pt, "").split($t).forEach((n) => {
    if (n) {
      const r = n.split(Mt);
      r.length > 1 && (t[r[0].trim()] = r[1].trim());
    }
  }), t;
}
function Ae(e) {
  let t = "";
  if (x(e))
    t = e;
  else if (g(e))
    for (let n = 0; n < e.length; n++) {
      const r = Ae(e[n]);
      r && (t += r + " ");
    }
  else if (v(e))
    for (const n in e)
      e[n] && (t += n + " ");
  return t.trim();
}
/**
* @vue/reactivity v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
function j(e, ...t) {
  console.warn(`[Vue warn] ${e}`, ...t);
}
let h, Xe = 0, J, q;
function Ht(e, t = !1) {
  if (e.flags |= 8, t) {
    e.next = q, q = e;
    return;
  }
  e.next = J, J = e;
}
function $e() {
  Xe++;
}
function Me() {
  if (--Xe > 0)
    return;
  if (q) {
    let t = q;
    for (q = void 0; t; ) {
      const n = t.next;
      t.next = void 0, t.flags &= -9, t = n;
    }
  }
  let e;
  for (; J; ) {
    let t = J;
    for (J = void 0; t; ) {
      const n = t.next;
      if (t.next = void 0, t.flags &= -9, t.flags & 1)
        try {
          t.trigger();
        } catch (r) {
          e || (e = r);
        }
      t = n;
    }
  }
  if (e) throw e;
}
function jt(e) {
  for (let t = e.deps; t; t = t.nextDep)
    t.version = -1, t.prevActiveLink = t.dep.activeLink, t.dep.activeLink = t;
}
function Lt(e) {
  let t, n = e.depsTail, r = n;
  for (; r; ) {
    const s = r.prevDep;
    r.version === -1 ? (r === n && (n = s), et(r), kt(r)) : t = r, r.dep.activeLink = r.prevActiveLink, r.prevActiveLink = void 0, r = s;
  }
  e.deps = t, e.depsTail = n;
}
function Kt(e) {
  for (let t = e.deps; t; t = t.nextDep)
    if (t.dep.version !== t.version || t.dep.computed && (Ze(t.dep.computed) || t.dep.version !== t.version))
      return !0;
  return !!e._dirty;
}
function Ze(e) {
  if (e.flags & 4 && !(e.flags & 16) || (e.flags &= -17, e.globalVersion === G) || (e.globalVersion = G, !e.isSSR && e.flags & 128 && (!e.deps && !e._dirty || !Kt(e))))
    return;
  e.flags |= 2;
  const t = e.dep, n = h, r = M;
  h = e, M = !0;
  try {
    jt(e);
    const s = e.fn(e._value);
    (t.version === 0 || A(s, e._value)) && (e.flags |= 128, e._value = s, t.version++);
  } catch (s) {
    throw t.version++, s;
  } finally {
    h = n, M = r, Lt(e), e.flags &= -3;
  }
}
function et(e, t = !1) {
  const { dep: n, prevSub: r, nextSub: s } = e;
  if (r && (r.nextSub = s, e.prevSub = void 0), s && (s.prevSub = r, e.nextSub = void 0), process.env.NODE_ENV !== "production" && n.subsHead === e && (n.subsHead = s), n.subs === e && (n.subs = r, !r && n.computed)) {
    n.computed.flags &= -5;
    for (let o = n.computed.deps; o; o = o.nextDep)
      et(o, !0);
  }
  !t && !--n.sc && n.map && n.map.delete(n.key);
}
function kt(e) {
  const { prevDep: t, nextDep: n } = e;
  t && (t.nextDep = n, e.prevDep = void 0), n && (n.prevDep = t, e.nextDep = void 0);
}
let M = !0;
const tt = [];
function ne() {
  tt.push(M), M = !1;
}
function se() {
  const e = tt.pop();
  M = e === void 0 ? !0 : e;
}
let G = 0;
class Wt {
  constructor(t, n) {
    this.sub = t, this.dep = n, this.version = n.version, this.nextDep = this.prevDep = this.nextSub = this.prevSub = this.prevActiveLink = void 0;
  }
}
class Pe {
  // TODO isolatedDeclarations "__v_skip"
  constructor(t) {
    this.computed = t, this.version = 0, this.activeLink = void 0, this.subs = void 0, this.map = void 0, this.key = void 0, this.sc = 0, this.__v_skip = !0, process.env.NODE_ENV !== "production" && (this.subsHead = void 0);
  }
  track(t) {
    if (!h || !M || h === this.computed)
      return;
    let n = this.activeLink;
    if (n === void 0 || n.sub !== h)
      n = this.activeLink = new Wt(h, this), h.deps ? (n.prevDep = h.depsTail, h.depsTail.nextDep = n, h.depsTail = n) : h.deps = h.depsTail = n, nt(n);
    else if (n.version === -1 && (n.version = this.version, n.nextDep)) {
      const r = n.nextDep;
      r.prevDep = n.prevDep, n.prevDep && (n.prevDep.nextDep = r), n.prevDep = h.depsTail, n.nextDep = void 0, h.depsTail.nextDep = n, h.depsTail = n, h.deps === n && (h.deps = r);
    }
    return process.env.NODE_ENV !== "production" && h.onTrack && h.onTrack(
      P(
        {
          effect: h
        },
        t
      )
    ), n;
  }
  trigger(t) {
    this.version++, G++, this.notify(t);
  }
  notify(t) {
    $e();
    try {
      if (process.env.NODE_ENV !== "production")
        for (let n = this.subsHead; n; n = n.nextSub)
          n.sub.onTrigger && !(n.sub.flags & 8) && n.sub.onTrigger(
            P(
              {
                effect: n.sub
              },
              t
            )
          );
      for (let n = this.subs; n; n = n.prevSub)
        n.sub.notify() && n.sub.dep.notify();
    } finally {
      Me();
    }
  }
}
function nt(e) {
  if (e.dep.sc++, e.sub.flags & 4) {
    const t = e.dep.computed;
    if (t && !e.dep.subs) {
      t.flags |= 20;
      for (let r = t.deps; r; r = r.nextDep)
        nt(r);
    }
    const n = e.dep.subs;
    n !== e && (e.prevSub = n, n && (n.nextSub = e)), process.env.NODE_ENV !== "production" && e.dep.subsHead === void 0 && (e.dep.subsHead = e), e.dep.subs = e;
  }
}
const Se = /* @__PURE__ */ new WeakMap(), F = /* @__PURE__ */ Symbol(
  process.env.NODE_ENV !== "production" ? "Object iterate" : ""
), xe = /* @__PURE__ */ Symbol(
  process.env.NODE_ENV !== "production" ? "Map keys iterate" : ""
), Q = /* @__PURE__ */ Symbol(
  process.env.NODE_ENV !== "production" ? "Array iterate" : ""
);
function m(e, t, n) {
  if (M && h) {
    let r = Se.get(e);
    r || Se.set(e, r = /* @__PURE__ */ new Map());
    let s = r.get(n);
    s || (r.set(n, s = new Pe()), s.map = r, s.key = n), process.env.NODE_ENV !== "production" ? s.track({
      target: e,
      type: t,
      key: n
    }) : s.track();
  }
}
function $(e, t, n, r, s, o) {
  const i = Se.get(e);
  if (!i) {
    G++;
    return;
  }
  const c = (a) => {
    a && (process.env.NODE_ENV !== "production" ? a.trigger({
      target: e,
      type: t,
      key: n,
      newValue: r,
      oldValue: s,
      oldTarget: o
    }) : a.trigger());
  };
  if ($e(), t === "clear")
    i.forEach(c);
  else {
    const a = g(e), u = a && Ie(n);
    if (a && n === "length") {
      const d = Number(r);
      i.forEach((l, f) => {
        (f === "length" || f === Q || !te(f) && f >= d) && c(l);
      });
    } else
      switch ((n !== void 0 || i.has(void 0)) && c(i.get(n)), u && c(i.get(Q)), t) {
        case "add":
          a ? u && c(i.get("length")) : (c(i.get(F)), Y(e) && c(i.get(xe)));
          break;
        case "delete":
          a || (c(i.get(F)), Y(e) && c(i.get(xe)));
          break;
        case "set":
          Y(e) && c(i.get(F));
          break;
      }
  }
  Me();
}
function L(e) {
  const t = /* @__PURE__ */ p(e);
  return t === e ? t : (m(t, "iterate", Q), /* @__PURE__ */ N(e) ? t : t.map(I));
}
function Fe(e) {
  return m(e = /* @__PURE__ */ p(e), "iterate", Q), e;
}
function S(e, t) {
  return /* @__PURE__ */ O(e) ? X(/* @__PURE__ */ He(e) ? I(t) : t) : I(t);
}
const Ut = {
  __proto__: null,
  [Symbol.iterator]() {
    return ge(this, Symbol.iterator, (e) => S(this, e));
  },
  concat(...e) {
    return L(this).concat(
      ...e.map((t) => g(t) ? L(t) : t)
    );
  },
  entries() {
    return ge(this, "entries", (e) => (e[1] = S(this, e[1]), e));
  },
  every(e, t) {
    return R(this, "every", e, t, void 0, arguments);
  },
  filter(e, t) {
    return R(
      this,
      "filter",
      e,
      t,
      (n) => n.map((r) => S(this, r)),
      arguments
    );
  },
  find(e, t) {
    return R(
      this,
      "find",
      e,
      t,
      (n) => S(this, n),
      arguments
    );
  },
  findIndex(e, t) {
    return R(this, "findIndex", e, t, void 0, arguments);
  },
  findLast(e, t) {
    return R(
      this,
      "findLast",
      e,
      t,
      (n) => S(this, n),
      arguments
    );
  },
  findLastIndex(e, t) {
    return R(this, "findLastIndex", e, t, void 0, arguments);
  },
  // flat, flatMap could benefit from ARRAY_ITERATE but are not straight-forward to implement
  forEach(e, t) {
    return R(this, "forEach", e, t, void 0, arguments);
  },
  includes(...e) {
    return me(this, "includes", e);
  },
  indexOf(...e) {
    return me(this, "indexOf", e);
  },
  join(e) {
    return L(this).join(e);
  },
  // keys() iterator only reads `length`, no optimization required
  lastIndexOf(...e) {
    return me(this, "lastIndexOf", e);
  },
  map(e, t) {
    return R(this, "map", e, t, void 0, arguments);
  },
  pop() {
    return z(this, "pop");
  },
  push(...e) {
    return z(this, "push", e);
  },
  reduce(e, ...t) {
    return Ue(this, "reduce", e, t);
  },
  reduceRight(e, ...t) {
    return Ue(this, "reduceRight", e, t);
  },
  shift() {
    return z(this, "shift");
  },
  // slice could use ARRAY_ITERATE but also seems to beg for range tracking
  some(e, t) {
    return R(this, "some", e, t, void 0, arguments);
  },
  splice(...e) {
    return z(this, "splice", e);
  },
  toReversed() {
    return L(this).toReversed();
  },
  toSorted(e) {
    return L(this).toSorted(e);
  },
  toSpliced(...e) {
    return L(this).toSpliced(...e);
  },
  unshift(...e) {
    return z(this, "unshift", e);
  },
  values() {
    return ge(this, "values", (e) => S(this, e));
  }
};
function ge(e, t, n) {
  const r = Fe(e), s = r[t]();
  return r !== e && !/* @__PURE__ */ N(e) && (s._next = s.next, s.next = () => {
    const o = s._next();
    return o.done || (o.value = n(o.value)), o;
  }), s;
}
const zt = Array.prototype;
function R(e, t, n, r, s, o) {
  const i = Fe(e), c = i !== e && !/* @__PURE__ */ N(e), a = i[t];
  if (a !== zt[t]) {
    const l = a.apply(e, o);
    return c ? I(l) : l;
  }
  let u = n;
  i !== e && (c ? u = function(l, f) {
    return n.call(this, S(e, l), f, e);
  } : n.length > 2 && (u = function(l, f) {
    return n.call(this, l, f, e);
  }));
  const d = a.call(i, u, r);
  return c && s ? s(d) : d;
}
function Ue(e, t, n, r) {
  const s = Fe(e), o = s !== e && !/* @__PURE__ */ N(e);
  let i = n, c = !1;
  s !== e && (o ? (c = r.length === 0, i = function(u, d, l) {
    return c && (c = !1, u = S(e, u)), n.call(this, u, S(e, d), l, e);
  }) : n.length > 3 && (i = function(u, d, l) {
    return n.call(this, u, d, l, e);
  }));
  const a = s[t](i, ...r);
  return c ? S(e, a) : a;
}
function me(e, t, n) {
  const r = /* @__PURE__ */ p(e);
  m(r, "iterate", Q);
  const s = r[t](...n);
  return (s === -1 || s === !1) && /* @__PURE__ */ ae(n[0]) ? (n[0] = /* @__PURE__ */ p(n[0]), r[t](...n)) : s;
}
function z(e, t, n = []) {
  ne(), $e();
  const r = (/* @__PURE__ */ p(e))[t].apply(e, n);
  return Me(), se(), r;
}
const Bt = /* @__PURE__ */ Dt("__proto__,__v_isRef,__isVue"), st = new Set(
  /* @__PURE__ */ Object.getOwnPropertyNames(Symbol).filter((e) => e !== "arguments" && e !== "caller").map((e) => Symbol[e]).filter(te)
);
function Yt(e) {
  te(e) || (e = String(e));
  const t = /* @__PURE__ */ p(this);
  return m(t, "has", e), t.hasOwnProperty(e);
}
class rt {
  constructor(t = !1, n = !1) {
    this._isReadonly = t, this._isShallow = n;
  }
  get(t, n, r) {
    if (n === "__v_skip") return t.__v_skip;
    const s = this._isReadonly, o = this._isShallow;
    if (n === "__v_isReactive")
      return !s;
    if (n === "__v_isReadonly")
      return s;
    if (n === "__v_isShallow")
      return o;
    if (n === "__v_raw")
      return r === (s ? o ? sn : ct : o ? nn : it).get(t) || // receiver is not the reactive proxy, but has the same prototype
      // this means the receiver is a user proxy of the reactive proxy
      Object.getPrototypeOf(t) === Object.getPrototypeOf(r) ? t : void 0;
    const i = g(t);
    if (!s) {
      let a;
      if (i && (a = Ut[n]))
        return a;
      if (n === "hasOwnProperty")
        return Yt;
    }
    const c = Reflect.get(
      t,
      n,
      // if this is a proxy wrapping a ref, return methods using the raw ref
      // as receiver so that we don't have to call `toRaw` on the ref in all
      // its class methods
      /* @__PURE__ */ V(t) ? t : r
    );
    if ((te(n) ? st.has(n) : Bt(n)) || (s || m(t, "get", n), o))
      return c;
    if (/* @__PURE__ */ V(c)) {
      const a = i && Ie(n) ? c : c.value;
      return s && v(a) ? /* @__PURE__ */ De(a) : a;
    }
    return v(c) ? s ? /* @__PURE__ */ De(c) : /* @__PURE__ */ lt(c) : c;
  }
}
class Jt extends rt {
  constructor(t = !1) {
    super(!1, t);
  }
  set(t, n, r, s) {
    let o = t[n];
    const i = g(t) && Ie(n);
    if (!this._isShallow) {
      const u = /* @__PURE__ */ O(o);
      if (!/* @__PURE__ */ N(r) && !/* @__PURE__ */ O(r) && (o = /* @__PURE__ */ p(o), r = /* @__PURE__ */ p(r)), !i && /* @__PURE__ */ V(o) && !/* @__PURE__ */ V(r))
        return u ? (process.env.NODE_ENV !== "production" && j(
          `Set operation on key "${String(n)}" failed: target is readonly.`,
          t[n]
        ), !0) : (o.value = r, !0);
    }
    const c = i ? Number(n) < t.length : we(t, n), a = Reflect.set(
      t,
      n,
      r,
      /* @__PURE__ */ V(t) ? t : s
    );
    return t === /* @__PURE__ */ p(s) && a && (c ? A(r, o) && $(t, "set", n, r, o) : $(t, "add", n, r)), a;
  }
  deleteProperty(t, n) {
    const r = we(t, n), s = t[n], o = Reflect.deleteProperty(t, n);
    return o && r && $(t, "delete", n, void 0, s), o;
  }
  has(t, n) {
    const r = Reflect.has(t, n);
    return (!te(n) || !st.has(n)) && m(t, "has", n), r;
  }
  ownKeys(t) {
    return m(
      t,
      "iterate",
      g(t) ? "length" : F
    ), Reflect.ownKeys(t);
  }
}
class qt extends rt {
  constructor(t = !1) {
    super(!0, t);
  }
  set(t, n) {
    return process.env.NODE_ENV !== "production" && j(
      `Set operation on key "${String(n)}" failed: target is readonly.`,
      t
    ), !0;
  }
  deleteProperty(t, n) {
    return process.env.NODE_ENV !== "production" && j(
      `Delete operation on key "${String(n)}" failed: target is readonly.`,
      t
    ), !0;
  }
}
const Gt = /* @__PURE__ */ new Jt(), Qt = /* @__PURE__ */ new qt(), Oe = (e) => e, oe = (e) => Reflect.getPrototypeOf(e);
function Xt(e, t, n) {
  return function(...r) {
    const s = this.__v_raw, o = /* @__PURE__ */ p(s), i = Y(o), c = e === "entries" || e === Symbol.iterator && i, a = e === "keys" && i, u = s[e](...r), d = n ? Oe : t ? X : I;
    return !t && m(
      o,
      "iterate",
      a ? xe : F
    ), P(
      // inheriting all iterator properties
      Object.create(u),
      {
        // iterator protocol
        next() {
          const { value: l, done: f } = u.next();
          return f ? { value: l, done: f } : {
            value: c ? [d(l[0]), d(l[1])] : d(l),
            done: f
          };
        }
      }
    );
  };
}
function ie(e) {
  return function(...t) {
    if (process.env.NODE_ENV !== "production") {
      const n = t[0] ? `on key "${t[0]}" ` : "";
      j(
        `${Qe(e)} operation ${n}failed: target is readonly.`,
        /* @__PURE__ */ p(this)
      );
    }
    return e === "delete" ? !1 : e === "clear" ? void 0 : this;
  };
}
function Zt(e, t) {
  const n = {
    get(s) {
      const o = this.__v_raw, i = /* @__PURE__ */ p(o), c = /* @__PURE__ */ p(s);
      e || (A(s, c) && m(i, "get", s), m(i, "get", c));
      const { has: a } = oe(i), u = t ? Oe : e ? X : I;
      if (a.call(i, s))
        return u(o.get(s));
      if (a.call(i, c))
        return u(o.get(c));
      o !== i && o.get(s);
    },
    get size() {
      const s = this.__v_raw;
      return !e && m(/* @__PURE__ */ p(s), "iterate", F), s.size;
    },
    has(s) {
      const o = this.__v_raw, i = /* @__PURE__ */ p(o), c = /* @__PURE__ */ p(s);
      return e || (A(s, c) && m(i, "has", s), m(i, "has", c)), s === c ? o.has(s) : o.has(s) || o.has(c);
    },
    forEach(s, o) {
      const i = this, c = i.__v_raw, a = /* @__PURE__ */ p(c), u = t ? Oe : e ? X : I;
      return !e && m(a, "iterate", F), c.forEach((d, l) => s.call(o, u(d), u(l), i));
    }
  };
  return P(
    n,
    e ? {
      add: ie("add"),
      set: ie("set"),
      delete: ie("delete"),
      clear: ie("clear")
    } : {
      add(s) {
        const o = /* @__PURE__ */ p(this), i = oe(o), c = /* @__PURE__ */ p(s), a = !t && !/* @__PURE__ */ N(s) && !/* @__PURE__ */ O(s) ? c : s;
        return i.has.call(o, a) || A(s, a) && i.has.call(o, s) || A(c, a) && i.has.call(o, c) || (o.add(a), $(o, "add", a, a)), this;
      },
      set(s, o) {
        !t && !/* @__PURE__ */ N(o) && !/* @__PURE__ */ O(o) && (o = /* @__PURE__ */ p(o));
        const i = /* @__PURE__ */ p(this), { has: c, get: a } = oe(i);
        let u = c.call(i, s);
        u ? process.env.NODE_ENV !== "production" && ze(i, c, s) : (s = /* @__PURE__ */ p(s), u = c.call(i, s));
        const d = a.call(i, s);
        return i.set(s, o), u ? A(o, d) && $(i, "set", s, o, d) : $(i, "add", s, o), this;
      },
      delete(s) {
        const o = /* @__PURE__ */ p(this), { has: i, get: c } = oe(o);
        let a = i.call(o, s);
        a ? process.env.NODE_ENV !== "production" && ze(o, i, s) : (s = /* @__PURE__ */ p(s), a = i.call(o, s));
        const u = c ? c.call(o, s) : void 0, d = o.delete(s);
        return a && $(o, "delete", s, void 0, u), d;
      },
      clear() {
        const s = /* @__PURE__ */ p(this), o = s.size !== 0, i = process.env.NODE_ENV !== "production" ? Y(s) ? new Map(s) : new Set(s) : void 0, c = s.clear();
        return o && $(
          s,
          "clear",
          void 0,
          void 0,
          i
        ), c;
      }
    }
  ), [
    "keys",
    "values",
    "entries",
    Symbol.iterator
  ].forEach((s) => {
    n[s] = Xt(s, e, t);
  }), n;
}
function ot(e, t) {
  const n = Zt(e, t);
  return (r, s, o) => s === "__v_isReactive" ? !e : s === "__v_isReadonly" ? e : s === "__v_raw" ? r : Reflect.get(
    we(n, s) && s in r ? n : r,
    s,
    o
  );
}
const en = {
  get: /* @__PURE__ */ ot(!1, !1)
}, tn = {
  get: /* @__PURE__ */ ot(!0, !1)
};
function ze(e, t, n) {
  const r = /* @__PURE__ */ p(n);
  if (r !== n && t.call(e, r)) {
    const s = qe(e);
    j(
      `Reactive ${s} contains both the raw and reactive versions of the same object${s === "Map" ? " as keys" : ""}, which can lead to inconsistencies. Avoid differentiating between the raw and reactive versions of an object and only use the reactive version if possible.`
    );
  }
}
const it = /* @__PURE__ */ new WeakMap(), nn = /* @__PURE__ */ new WeakMap(), ct = /* @__PURE__ */ new WeakMap(), sn = /* @__PURE__ */ new WeakMap();
function rn(e) {
  switch (e) {
    case "Object":
    case "Array":
      return 1;
    case "Map":
    case "Set":
    case "WeakMap":
    case "WeakSet":
      return 2;
    default:
      return 0;
  }
}
// @__NO_SIDE_EFFECTS__
function lt(e) {
  return /* @__PURE__ */ O(e) ? e : at(
    e,
    !1,
    Gt,
    en,
    it
  );
}
// @__NO_SIDE_EFFECTS__
function De(e) {
  return at(
    e,
    !0,
    Qt,
    tn,
    ct
  );
}
function at(e, t, n, r, s) {
  if (!v(e))
    return process.env.NODE_ENV !== "production" && j(
      `value cannot be made ${t ? "readonly" : "reactive"}: ${String(
        e
      )}`
    ), e;
  if (e.__v_raw && !(t && e.__v_isReactive) || e.__v_skip || !Object.isExtensible(e))
    return e;
  const o = s.get(e);
  if (o)
    return o;
  const i = rn(qe(e));
  if (i === 0)
    return e;
  const c = new Proxy(
    e,
    i === 2 ? r : n
  );
  return s.set(e, c), c;
}
// @__NO_SIDE_EFFECTS__
function He(e) {
  return /* @__PURE__ */ O(e) ? /* @__PURE__ */ He(e.__v_raw) : !!(e && e.__v_isReactive);
}
// @__NO_SIDE_EFFECTS__
function O(e) {
  return !!(e && e.__v_isReadonly);
}
// @__NO_SIDE_EFFECTS__
function N(e) {
  return !!(e && e.__v_isShallow);
}
// @__NO_SIDE_EFFECTS__
function ae(e) {
  return e ? !!e.__v_raw : !1;
}
// @__NO_SIDE_EFFECTS__
function p(e) {
  const t = e && e.__v_raw;
  return t ? /* @__PURE__ */ p(t) : e;
}
const I = (e) => v(e) ? /* @__PURE__ */ lt(e) : e, X = (e) => v(e) ? /* @__PURE__ */ De(e) : e;
// @__NO_SIDE_EFFECTS__
function V(e) {
  return e ? e.__v_isRef === !0 : !1;
}
// @__NO_SIDE_EFFECTS__
function ve(e) {
  return on(e, !1);
}
function on(e, t) {
  return /* @__PURE__ */ V(e) ? e : new cn(e, t);
}
class cn {
  constructor(t, n) {
    this.dep = new Pe(), this.__v_isRef = !0, this.__v_isShallow = !1, this._rawValue = n ? t : /* @__PURE__ */ p(t), this._value = n ? t : I(t), this.__v_isShallow = n;
  }
  get value() {
    return process.env.NODE_ENV !== "production" ? this.dep.track({
      target: this,
      type: "get",
      key: "value"
    }) : this.dep.track(), this._value;
  }
  set value(t) {
    const n = this._rawValue, r = this.__v_isShallow || /* @__PURE__ */ N(t) || /* @__PURE__ */ O(t);
    t = r ? t : /* @__PURE__ */ p(t), A(t, n) && (this._rawValue = t, this._value = r ? t : I(t), process.env.NODE_ENV !== "production" ? this.dep.trigger({
      target: this,
      type: "set",
      key: "value",
      newValue: t,
      oldValue: n
    }) : this.dep.trigger());
  }
}
class ln {
  constructor(t, n, r) {
    this.fn = t, this.setter = n, this._value = void 0, this.dep = new Pe(this), this.__v_isRef = !0, this.deps = void 0, this.depsTail = void 0, this.flags = 16, this.globalVersion = G - 1, this.next = void 0, this.effect = this, this.__v_isReadonly = !n, this.isSSR = r;
  }
  /**
   * @internal
   */
  notify() {
    if (this.flags |= 16, !(this.flags & 8) && // avoid infinite self recursion
    h !== this)
      return Ht(this, !0), !0;
    process.env.NODE_ENV;
  }
  get value() {
    const t = process.env.NODE_ENV !== "production" ? this.dep.track({
      target: this,
      type: "get",
      key: "value"
    }) : this.dep.track();
    return Ze(this), t && (t.version = this.dep.version), this._value;
  }
  set value(t) {
    this.setter ? this.setter(t) : process.env.NODE_ENV !== "production" && j("Write operation failed: computed value is readonly");
  }
}
// @__NO_SIDE_EFFECTS__
function an(e, t, n = !1) {
  let r, s;
  b(e) ? r = e : (r = e.get, s = e.set);
  const o = new ln(r, s, n);
  return process.env.NODE_ENV, o;
}
/**
* @vue/runtime-core v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
const H = [];
function un(e) {
  H.push(e);
}
function fn() {
  H.pop();
}
let be = !1;
function D(e, ...t) {
  if (be) return;
  be = !0, ne();
  const n = H.length ? H[H.length - 1].component : null, r = n && n.appContext.config.warnHandler, s = pn();
  if (r)
    _e(
      r,
      n,
      11,
      [
        // eslint-disable-next-line no-restricted-syntax
        e + t.map((o) => {
          var i, c;
          return (c = (i = o.toString) == null ? void 0 : i.call(o)) != null ? c : JSON.stringify(o);
        }).join(""),
        n && n.proxy,
        s.map(
          ({ vnode: o }) => `at <${xt(n, o.type)}>`
        ).join(`
`),
        s
      ]
    );
  else {
    const o = [`[Vue warn]: ${e}`, ...t];
    s.length && o.push(`
`, ...dn(s)), console.warn(...o);
  }
  se(), be = !1;
}
function pn() {
  let e = H[H.length - 1];
  if (!e)
    return [];
  const t = [];
  for (; e; ) {
    const n = t[0];
    n && n.vnode === e ? n.recurseCount++ : t.push({
      vnode: e,
      recurseCount: 0
    });
    const r = e.component && e.component.parent;
    e = r && r.vnode;
  }
  return t;
}
function dn(e) {
  const t = [];
  return e.forEach((n, r) => {
    t.push(...r === 0 ? [] : [`
`], ...hn(n));
  }), t;
}
function hn({ vnode: e, recurseCount: t }) {
  const n = t > 0 ? `... (${t} recursive calls)` : "", r = e.component ? e.component.parent == null : !1, s = ` at <${xt(
    e.component,
    e.type,
    r
  )}`, o = ">" + n;
  return e.props ? [s, ..._n(e.props), o] : [s + o];
}
function _n(e) {
  const t = [], n = Object.keys(e);
  return n.slice(0, 3).forEach((r) => {
    t.push(...ut(r, e[r]));
  }), n.length > 3 && t.push(" ..."), t;
}
function ut(e, t, n) {
  return x(t) ? (t = JSON.stringify(t), n ? t : [`${e}=${t}`]) : typeof t == "number" || typeof t == "boolean" || t == null ? n ? t : [`${e}=${t}`] : /* @__PURE__ */ V(t) ? (t = ut(e, /* @__PURE__ */ p(t.value), !0), n ? t : [`${e}=Ref<`, t, ">"]) : b(t) ? [`${e}=fn${t.name ? `<${t.name}>` : ""}`] : (t = /* @__PURE__ */ p(t), n ? t : [`${e}=`, t]);
}
const je = {
  sp: "serverPrefetch hook",
  bc: "beforeCreate hook",
  c: "created hook",
  bm: "beforeMount hook",
  m: "mounted hook",
  bu: "beforeUpdate hook",
  u: "updated",
  bum: "beforeUnmount hook",
  um: "unmounted hook",
  a: "activated hook",
  da: "deactivated hook",
  ec: "errorCaptured hook",
  rtc: "renderTracked hook",
  rtg: "renderTriggered hook",
  0: "setup function",
  1: "render function",
  2: "watcher getter",
  3: "watcher callback",
  4: "watcher cleanup function",
  5: "native event handler",
  6: "component event handler",
  7: "vnode hook",
  8: "directive hook",
  9: "transition hook",
  10: "app errorHandler",
  11: "app warnHandler",
  12: "ref function",
  13: "async component loader",
  14: "scheduler flush",
  15: "component update",
  16: "app unmount cleanup function"
};
function _e(e, t, n, r) {
  try {
    return r ? e(...r) : e();
  } catch (s) {
    Le(s, t, n);
  }
}
function ft(e, t, n, r) {
  if (b(e)) {
    const s = _e(e, t, n, r);
    return s && It(s) && s.catch((o) => {
      Le(o, t, n);
    }), s;
  }
  if (g(e)) {
    const s = [];
    for (let o = 0; o < e.length; o++)
      s.push(ft(e[o], t, n, r));
    return s;
  } else process.env.NODE_ENV !== "production" && D(
    `Invalid value type passed to callWithAsyncErrorHandling(): ${typeof e}`
  );
}
function Le(e, t, n, r = !0) {
  const s = t ? t.vnode : null, { errorHandler: o, throwUnhandledErrorInProduction: i } = t && t.appContext.config || Ne;
  if (t) {
    let c = t.parent;
    const a = t.proxy, u = process.env.NODE_ENV !== "production" ? je[n] : `https://vuejs.org/error-reference/#runtime-${n}`;
    for (; c; ) {
      const d = c.ec;
      if (d) {
        for (let l = 0; l < d.length; l++)
          if (d[l](e, a, u) === !1)
            return;
      }
      c = c.parent;
    }
    if (o) {
      ne(), _e(o, null, 10, [
        e,
        a,
        u
      ]), se();
      return;
    }
  }
  gn(e, n, s, r, i);
}
function gn(e, t, n, r = !0, s = !1) {
  if (process.env.NODE_ENV !== "production") {
    const o = je[t];
    if (n && un(n), D(`Unhandled error${o ? ` during execution of ${o}` : ""}`), n && fn(), r)
      throw e;
    console.error(e);
  } else {
    if (s)
      throw e;
    console.error(e);
  }
}
const y = [];
let T = -1;
const W = [];
let C = null, K = 0;
const mn = /* @__PURE__ */ Promise.resolve();
let Re = null;
const vn = 100;
function bn(e) {
  let t = T + 1, n = y.length;
  for (; t < n; ) {
    const r = t + n >>> 1, s = y[r], o = Z(s);
    o < e || o === e && s.flags & 2 ? t = r + 1 : n = r;
  }
  return t;
}
function En(e) {
  if (!(e.flags & 1)) {
    const t = Z(e), n = y[y.length - 1];
    !n || // fast path when the job id is larger than the tail
    !(e.flags & 2) && t >= Z(n) ? y.push(e) : y.splice(bn(t), 0, e), e.flags |= 1, pt();
  }
}
function pt() {
  Re || (Re = mn.then(dt));
}
function yn(e) {
  if (!g(e))
    C && e.id === -1 ? C.splice(K + 1, 0, e) : e.flags & 1 || (W.push(e), e.flags |= 1);
  else
    for (let t = 0; t < e.length; t++)
      W.push(e[t]);
  pt();
}
function Nn(e) {
  if (W.length) {
    const t = [...new Set(W)].sort(
      (n, r) => Z(n) - Z(r)
    );
    if (W.length = 0, C) {
      for (let n = 0; n < t.length; n++)
        C.push(t[n]);
      return;
    }
    for (C = t, process.env.NODE_ENV !== "production" && (e = e || /* @__PURE__ */ new Map()), K = 0; K < C.length; K++) {
      const n = C[K];
      process.env.NODE_ENV !== "production" && ht(e, n) || (n.flags & 4 && (n.flags &= -2), n.flags & 8 || n(), n.flags &= -2);
    }
    C = null, K = 0;
  }
}
const Z = (e) => e.id == null ? e.flags & 2 ? -1 : 1 / 0 : e.id;
function dt(e) {
  process.env.NODE_ENV !== "production" && (e = e || /* @__PURE__ */ new Map());
  const t = process.env.NODE_ENV !== "production" ? (n) => ht(e, n) : Ye;
  try {
    for (T = 0; T < y.length; T++) {
      const n = y[T];
      if (n && !(n.flags & 8)) {
        if (process.env.NODE_ENV !== "production" && t(n))
          continue;
        n.flags & 4 && (n.flags &= -2), _e(
          n,
          n.i,
          n.i ? 15 : 14
        ), n.flags & 4 || (n.flags &= -2);
      }
    }
  } finally {
    for (; T < y.length; T++) {
      const n = y[T];
      n && (n.flags &= -2);
    }
    T = -1, y.length = 0, Nn(e), Re = null, (y.length || W.length) && dt(e);
  }
}
function ht(e, t) {
  const n = e.get(t) || 0;
  if (n > vn) {
    const r = t.i, s = r && St(r.type);
    return Le(
      `Maximum recursive updates exceeded${s ? ` in component <${s}>` : ""}. This means you have a reactive effect that is mutating its own dependencies and thus recursively triggering itself. Possible sources include component template, render function, updated hook or watcher source function.`,
      null,
      10
    ), !0;
  }
  return e.set(t, n + 1), !1;
}
const Ee = /* @__PURE__ */ new Map();
process.env.NODE_ENV !== "production" && (he().__VUE_HMR_RUNTIME__ = {
  createRecord: ye(wn),
  rerender: ye(Sn),
  reload: ye(xn)
});
const ue = /* @__PURE__ */ new Map();
function wn(e, t) {
  return ue.has(e) ? !1 : (ue.set(e, {
    initialDef: fe(t),
    instances: /* @__PURE__ */ new Set()
  }), !0);
}
function fe(e) {
  return Ot(e) ? e.__vccOpts : e;
}
function Sn(e, t) {
  const n = ue.get(e);
  n && (n.initialDef.render = t, [...n.instances].forEach((r) => {
    t && (r.render = t, fe(r.type).render = t), r.renderCache = [], r.job.flags & 8 || r.update();
  }));
}
function xn(e, t) {
  const n = ue.get(e);
  if (!n) return;
  t = fe(t), Be(n.initialDef, t);
  const r = [...n.instances];
  for (let s = 0; s < r.length; s++) {
    const o = r[s], i = fe(o.type);
    let c = Ee.get(i);
    c || (i !== n.initialDef && Be(i, t), Ee.set(i, c = /* @__PURE__ */ new Set())), c.add(o), o.appContext.propsCache.delete(o.type), o.appContext.emitsCache.delete(o.type), o.appContext.optionsCache.delete(o.type), o.ceReload ? (c.add(o), o.ceReload(t.styles), c.delete(o)) : o.parent ? En(() => {
      o.job.flags & 8 || (o.parent.update(), c.delete(o));
    }) : o.appContext.reload ? o.appContext.reload() : typeof window < "u" ? window.location.reload() : console.warn(
      "[HMR] Root or manually mounted instance modified. Full reload required."
    ), o.root.ce && o !== o.root && o.root.ce._removeChildStyle(i);
  }
  yn(() => {
    Ee.clear();
  });
}
function Be(e, t) {
  P(e, t);
  for (const n in e)
    n !== "__file" && !(n in t) && delete e[n];
}
function ye(e) {
  return (t, n) => {
    try {
      return e(t, n);
    } catch (r) {
      console.error(r), console.warn(
        "[HMR] Something went wrong during Vue component hot-reload. Full reload required."
      );
    }
  };
}
let k, ce = [];
function _t(e, t) {
  var n, r;
  k = e, k ? (k.enabled = !0, ce.forEach(({ event: s, args: o }) => k.emit(s, ...o)), ce = []) : /* handle late devtools injection - only do this if we are in an actual */ /* browser environment to avoid the timer handle stalling test runner exit */ /* (#4815) */ typeof window < "u" && // some envs mock window but not fully
  window.HTMLElement && // also exclude jsdom
  // eslint-disable-next-line no-restricted-syntax
  !((r = (n = window.navigator) == null ? void 0 : n.userAgent) != null && r.includes("jsdom")) ? ((t.__VUE_DEVTOOLS_HOOK_REPLAY__ = t.__VUE_DEVTOOLS_HOOK_REPLAY__ || []).push((o) => {
    _t(o, t);
  }), setTimeout(() => {
    k || (t.__VUE_DEVTOOLS_HOOK_REPLAY__ = null, ce = []);
  }, 3e3)) : ce = [];
}
let ee = null, On = null;
const Ke = (e) => e.__isTeleport;
function Dn(e) {
  let t = e[0];
  if (e.length > 1) {
    let n = !1;
    for (const r of e)
      if (r.type !== Et) {
        if (process.env.NODE_ENV !== "production" && n) {
          D(
            "<transition> can only be used on a single element or component. Use <transition-group> for lists."
          );
          break;
        }
        if (t = r, n = !0, process.env.NODE_ENV === "production") break;
      }
  }
  return t;
}
function Rn(e) {
  if (!Tn(e))
    return Ke(e.type) && e.children ? Dn(e.children) : e;
  if (e.component)
    return e.component.subTree;
  const { shapeFlag: t, children: n } = e;
  if (n) {
    if (t & 16)
      return n[0];
    if (t & 32 && b(n.default))
      return n.default();
  }
}
function gt(e, t) {
  if (e.shapeFlag & 6 && e.component) {
    e.transition = t;
    const n = e.component.subTree;
    gt(
      Ke(n.type) && Rn(n) || n,
      t
    );
  } else e.shapeFlag & 128 ? (e.ssContent.transition = t.clone(e.ssContent), e.ssFallback.transition = t.clone(e.ssFallback)) : e.transition = t;
}
he().requestIdleCallback;
he().cancelIdleCallback;
const Tn = (e) => e.type.__isKeepAlive;
function Vn(e, t, n = re, r = !1) {
  if (n) {
    const s = n[e] || (n[e] = []), o = t.__weh || (t.__weh = (...i) => {
      ne();
      const c = zn(n), a = ft(t, n, e, i);
      return c(), se(), a;
    });
    return r ? s.unshift(o) : s.push(o), o;
  } else if (process.env.NODE_ENV !== "production") {
    const s = At(je[e].replace(/ hook$/, ""));
    D(
      `${s} is called when there is no active component instance to be associated with. Lifecycle injection APIs can only be used during execution of setup(). If you are using async setup(), make sure to register lifecycle hooks before the first await statement.`
    );
  }
}
const mt = (e) => (t, n = re) => {
  (!ke || e === "sp") && Vn(e, (...r) => t(...r), n);
}, In = mt("m"), Cn = mt("um"), An = /* @__PURE__ */ Symbol.for("v-ndc"), $n = {};
process.env.NODE_ENV !== "production" && ($n.ownKeys = (e) => (D(
  "Avoid app logic that relies on enumerating keys on a component instance. The keys will be empty in production mode to avoid performance overhead."
), Reflect.ownKeys(e)));
const Mn = {}, vt = (e) => Object.getPrototypeOf(e) === Mn, Pn = (e) => e.__isSuspense, bt = /* @__PURE__ */ Symbol.for("v-fgt"), Fn = /* @__PURE__ */ Symbol.for("v-txt"), Et = /* @__PURE__ */ Symbol.for("v-cmt");
function Te(e) {
  return e ? e.__v_isVNode === !0 : !1;
}
const Hn = (...e) => Nt(
  ...e
), yt = ({ key: e }) => e ?? null, le = ({
  ref: e,
  ref_key: t,
  ref_for: n
}) => (typeof e == "number" && (e = "" + e), e != null ? x(e) || /* @__PURE__ */ V(e) || b(e) ? { i: ee, r: e, k: t, f: !!n } : e : null);
function jn(e, t = null, n = null, r = 0, s = null, o = e === bt ? 0 : 1, i = !1, c = !1) {
  const a = {
    __v_isVNode: !0,
    __v_skip: !0,
    type: e,
    props: t,
    key: t && yt(t),
    ref: t && le(t),
    scopeId: On,
    slotScopeIds: null,
    children: n,
    component: null,
    suspense: null,
    ssContent: null,
    ssFallback: null,
    dirs: null,
    transition: null,
    el: null,
    anchor: null,
    target: null,
    targetStart: null,
    targetAnchor: null,
    staticCount: 0,
    shapeFlag: o,
    patchFlag: r,
    dynamicProps: s,
    dynamicChildren: null,
    appContext: null,
    ctx: ee
  };
  if (c ? (de(a, n), o & 128 && e.normalize(a)) : n && (a.shapeFlag |= x(n) ? 8 : 16), process.env.NODE_ENV !== "production" && a.key !== a.key && D("VNode created with invalid key (NaN). VNode type:", a.type), process.env.NODE_ENV !== "production" && t && a.shapeFlag & 1) {
    const u = t.innerHTML != null ? "innerHTML" : t.textContent != null ? "textContent" : null;
    u && Ln(a.children) && D(
      `The \`${u}\` prop on <${a.type}> will override its children. Remove either the \`${u}\` prop or the children.`
    );
  }
  return a;
}
function Ln(e) {
  return x(e) ? e !== "" : g(e) ? e.length > 0 : !1;
}
const B = process.env.NODE_ENV !== "production" ? Hn : Nt;
function Nt(e, t = null, n = null, r = 0, s = null, o = !1) {
  if ((!e || e === An) && (process.env.NODE_ENV !== "production" && !e && D(`Invalid vnode type when creating vnode: ${e}.`), e = Et), Te(e)) {
    const c = pe(
      e,
      t,
      !0
      /* mergeRef: true */
    );
    return n && de(c, n), c.patchFlag = -2, c;
  }
  if (Ot(e) && (e = e.__vccOpts), t) {
    t = Kn(t);
    let { class: c, style: a } = t;
    c && !x(c) && (t.class = Ae(c)), v(a) && (/* @__PURE__ */ ae(a) && !g(a) && (a = P({}, a)), t.style = Ce(a));
  }
  const i = x(e) ? 1 : Pn(e) ? 128 : Ke(e) ? 64 : v(e) ? 4 : b(e) ? 2 : 0;
  return process.env.NODE_ENV !== "production" && i & 4 && /* @__PURE__ */ ae(e) && (e = /* @__PURE__ */ p(e), D(
    "Vue received a Component that was made a reactive object. This can lead to unnecessary performance overhead and should be avoided by marking the component with `markRaw` or using `shallowRef` instead of `ref`.",
    `
Component that was made reactive: `,
    e
  )), jn(
    e,
    t,
    n,
    r,
    s,
    i,
    o,
    !0
  );
}
function Kn(e) {
  return e ? /* @__PURE__ */ ae(e) || vt(e) ? P({}, e) : e : null;
}
function pe(e, t, n = !1, r = !1) {
  const { props: s, ref: o, patchFlag: i, children: c, transition: a } = e, u = t ? Wn(s || {}, t) : s, d = {
    __v_isVNode: !0,
    __v_skip: !0,
    type: e.type,
    props: u,
    key: u && yt(u),
    ref: t && t.ref ? (
      // #2078 in the case of <component :is="vnode" ref="extra"/>
      // if the vnode itself already has a ref, cloneVNode will need to merge
      // the refs so the single vnode can be set on multiple refs
      n && o ? g(o) ? o.concat(le(t)) : [o, le(t)] : le(t)
    ) : o,
    scopeId: e.scopeId,
    slotScopeIds: e.slotScopeIds,
    children: process.env.NODE_ENV !== "production" && i === -1 && g(c) ? c.map(wt) : c,
    target: e.target,
    targetStart: e.targetStart,
    targetAnchor: e.targetAnchor,
    staticCount: e.staticCount,
    shapeFlag: e.shapeFlag,
    // if the vnode is cloned with extra props, we can no longer assume its
    // existing patch flag to be reliable and need to add the FULL_PROPS flag.
    // note: preserve flag for fragments since they use the flag for children
    // fast paths only.
    patchFlag: t && e.type !== bt ? i === -1 ? 16 : i | 16 : i,
    dynamicProps: e.dynamicProps,
    dynamicChildren: e.dynamicChildren,
    appContext: e.appContext,
    dirs: e.dirs,
    transition: a,
    // These should technically only be non-null on mounted VNodes. However,
    // they *should* be copied for kept-alive vnodes. So we just always copy
    // them since them being non-null during a mount doesn't affect the logic as
    // they will simply be overwritten.
    component: e.component,
    suspense: e.suspense,
    ssContent: e.ssContent && pe(e.ssContent),
    ssFallback: e.ssFallback && pe(e.ssFallback),
    placeholder: e.placeholder,
    el: e.el,
    anchor: e.anchor,
    ctx: e.ctx,
    ce: e.ce
  };
  return a && r && gt(
    d,
    a.clone(d)
  ), d;
}
function wt(e) {
  const t = pe(e);
  return g(e.children) && (t.children = e.children.map(wt)), t;
}
function kn(e = " ", t = 0) {
  return B(Fn, null, e, t);
}
function de(e, t) {
  let n = 0;
  const { shapeFlag: r } = e;
  if (t == null)
    t = null;
  else if (g(t))
    n = 16;
  else if (typeof t == "object")
    if (r & 65) {
      const s = t.default;
      s && (s._c && (s._d = !1), de(e, s()), s._c && (s._d = !0));
      return;
    } else
      n = 32, !t._ && !vt(t) && (t._ctx = ee);
  else if (b(t)) {
    if (r & 65) {
      de(e, { default: t });
      return;
    }
    t = { default: t, _ctx: ee }, n = 32;
  } else
    t = String(t), r & 64 ? (n = 16, t = [kn(t)]) : n = 8;
  e.children = t, e.shapeFlag |= n;
}
function Wn(...e) {
  const t = {};
  for (let n = 0; n < e.length; n++) {
    const r = e[n];
    for (const s in r)
      if (s === "class")
        t.class !== r.class && (t.class = Ae([t.class, r.class]));
      else if (s === "style")
        t.style = Ce([t.style, r.style]);
      else if (Rt(s)) {
        const o = t[s], i = r[s];
        i && o !== i && !(g(o) && o.includes(i)) ? t[s] = o ? [].concat(o, i) : i : i == null && o == null && // mergeProps({ 'onUpdate:modelValue': undefined }) should not retain
        // the model listener.
        !Tt(s) && (t[s] = i);
      } else s !== "" && (t[s] = r[s]);
  }
  return t;
}
let re = null;
const Un = () => re || ee;
let Ve;
{
  const e = he(), t = (n, r) => {
    let s;
    return (s = e[n]) || (s = e[n] = []), s.push(r), (o) => {
      s.length > 1 ? s.forEach((i) => i(o)) : s[0](o);
    };
  };
  Ve = t(
    "__VUE_INSTANCE_SETTERS__",
    (n) => re = n
  ), t(
    "__VUE_SSR_SETTERS__",
    (n) => ke = n
  );
}
const zn = (e) => {
  const t = re;
  return Ve(e), e.scope.on(), () => {
    e.scope.off(), Ve(t);
  };
};
let ke = !1;
process.env.NODE_ENV;
const Bn = /(?:^|[-_])\w/g, Yn = (e) => e.replace(Bn, (t) => t.toUpperCase()).replace(/[-_]/g, "");
function St(e, t = !0) {
  return b(e) ? e.displayName || e.name : e.name || t && e.__name;
}
function xt(e, t, n = !1) {
  let r = St(t);
  if (!r && t.__file) {
    const s = t.__file.match(/([^/\\]+)\.\w+$/);
    s && (r = s[1]);
  }
  if (!r && e) {
    const s = (o) => {
      for (const i in o)
        if (o[i] === t)
          return i;
    };
    r = s(e.components) || e.parent && s(
      e.parent.type.components
    ) || s(e.appContext.components);
  }
  return r ? Yn(r) : n ? "App" : "Anonymous";
}
function Ot(e) {
  return b(e) && "__vccOpts" in e;
}
const Jn = (e, t) => {
  const n = /* @__PURE__ */ an(e, t, ke);
  if (process.env.NODE_ENV !== "production") {
    const r = Un();
    r && r.appContext.config.warnRecursiveComputed && (n._warnRecursive = !0);
  }
  return n;
};
function _(e, t, n) {
  try {
    const r = arguments.length;
    return r === 2 ? v(t) && !g(t) ? Te(t) ? B(e, null, [t]) : B(e, t) : B(e, null, t) : (r > 3 ? n = Array.prototype.slice.call(arguments, 2) : r === 3 && Te(n) && (n = [n]), B(e, t, n));
  } finally {
  }
}
function qn() {
  if (process.env.NODE_ENV === "production" || typeof window > "u")
    return;
  const e = { style: "color:#3ba776" }, t = { style: "color:#1677ff" }, n = { style: "color:#f5222d" }, r = { style: "color:#eb2f96" }, s = {
    __vue_custom_formatter: !0,
    header(l) {
      if (!v(l))
        return null;
      if (l.__isVue)
        return ["div", e, "VueInstance"];
      if (/* @__PURE__ */ V(l)) {
        ne();
        const f = l.value;
        return se(), [
          "div",
          {},
          ["span", e, d(l)],
          "<",
          c(f),
          ">"
        ];
      } else {
        if (/* @__PURE__ */ He(l))
          return [
            "div",
            {},
            ["span", e, /* @__PURE__ */ N(l) ? "ShallowReactive" : "Reactive"],
            "<",
            c(l),
            `>${/* @__PURE__ */ O(l) ? " (readonly)" : ""}`
          ];
        if (/* @__PURE__ */ O(l))
          return [
            "div",
            {},
            ["span", e, /* @__PURE__ */ N(l) ? "ShallowReadonly" : "Readonly"],
            "<",
            c(l),
            ">"
          ];
      }
      return null;
    },
    hasBody(l) {
      return l && l.__isVue;
    },
    body(l) {
      if (l && l.__isVue)
        return [
          "div",
          {},
          ...o(l.$)
        ];
    }
  };
  function o(l) {
    const f = [];
    l.type.props && l.props && f.push(i("props", /* @__PURE__ */ p(l.props))), l.setupState !== Ne && f.push(i("setup", l.setupState)), l.data !== Ne && f.push(i("data", /* @__PURE__ */ p(l.data)));
    const E = a(l, "computed");
    E && f.push(i("computed", E));
    const w = a(l, "inject");
    return w && f.push(i("injected", w)), f.push([
      "div",
      {},
      [
        "span",
        {
          style: r.style + ";opacity:0.66"
        },
        "$ (internal): "
      ],
      ["object", { object: l }]
    ]), f;
  }
  function i(l, f) {
    return f = P({}, f), Object.keys(f).length ? [
      "div",
      { style: "line-height:1.25em;margin-bottom:0.6em" },
      [
        "div",
        {
          style: "color:#476582"
        },
        l
      ],
      [
        "div",
        {
          style: "padding-left:1.25em"
        },
        ...Object.keys(f).map((E) => [
          "div",
          {},
          ["span", r, E + ": "],
          c(f[E], !1)
        ])
      ]
    ] : ["span", {}];
  }
  function c(l, f = !0) {
    return typeof l == "number" ? ["span", t, l] : typeof l == "string" ? ["span", n, JSON.stringify(l)] : typeof l == "boolean" ? ["span", r, l] : v(l) ? ["object", { object: f ? /* @__PURE__ */ p(l) : l }] : ["span", n, String(l)];
  }
  function a(l, f) {
    const E = l.type;
    if (b(E))
      return;
    const w = {};
    for (const U in l.ctx)
      u(E, U, f) && (w[U] = l.ctx[U]);
    return w;
  }
  function u(l, f, E) {
    const w = l[E];
    if (g(w) && w.includes(f) || v(w) && f in w || l.extends && u(l.extends, f, E) || l.mixins && l.mixins.some((U) => u(U, f, E)))
      return !0;
  }
  function d(l) {
    return /* @__PURE__ */ N(l) ? "ShallowRef" : l.effect ? "ComputedRef" : "Ref";
  }
  window.devtoolsFormatters ? window.devtoolsFormatters.push(s) : window.devtoolsFormatters = [s];
}
process.env.NODE_ENV;
process.env.NODE_ENV;
process.env.NODE_ENV;
/**
* vue v3.5.42
* (c) 2018-present Yuxi (Evan) You and Vue contributors
* @license MIT
**/
function Gn() {
  qn();
}
process.env.NODE_ENV !== "production" && Gn();
const Qn = {
  pending: "待办",
  in_progress: "进行中",
  completed: "已完成",
  deleted: "已删"
}, Xn = {
  pending: "#7d838f",
  in_progress: "#d4a25c",
  completed: "#7dd87d",
  deleted: "#ff7b72"
}, es = {
  name: "TodoPanel",
  props: {
    state: { type: Object, default: () => ({}) }
  },
  setup() {
    const e = /* @__PURE__ */ ve([]), t = /* @__PURE__ */ ve(""), n = /* @__PURE__ */ ve(!0);
    let r = null;
    async function s() {
      try {
        const i = await fetch("/api/todo");
        if (!i.ok) {
          t.value = `HTTP ${i.status}`;
          return;
        }
        const c = await i.json();
        Array.isArray(c) ? (e.value = c, t.value = "") : t.value = c.error ?? "未知错误";
      } catch (i) {
        t.value = String(i);
      }
    }
    const o = Jn(() => {
      const i = { pending: 0, in_progress: 0, completed: 0 };
      for (const c of e.value)
        c.status in i && i[c.status]++;
      return i;
    });
    return In(() => {
      s(), r = setInterval(() => void s(), 5e3);
    }), Cn(() => {
      r && clearInterval(r);
    }), { todos: e, err: t, open: n, refresh: s, counts: o };
  },
  render() {
    const e = this, t = e.state ?? {}, n = _(
      "div",
      { style: { display: "flex", alignItems: "center", gap: "10px", fontSize: "12px", color: "#7d838f" } },
      [
        _("span", { style: { color: "#6eb3ff", fontWeight: 600 } }, "gah"),
        _("span", { style: { color: t.running ? "#d4a25c" : "#7d838f" } }, t.running ? "思考/执行" : "待输入"),
        _("span", {}, "模型 " + (t.model || "未设置")),
        _("span", {}, "[" + t.thinking + "]"),
        _("span", { style: { color: "#7dd87d" } }, `✓${e.counts.completed}`),
        _("span", { style: { color: "#d4a25c" } }, `▶${e.counts.in_progress}`),
        _("span", { onClick: () => {
          e.open = !e.open;
        } }, e.open ? "▾ 收起" : "▸ 展开")
      ]
    ), r = e.open ? _(
      "aside",
      {
        style: {
          position: "fixed",
          right: "14px",
          bottom: "44px",
          width: "300px",
          maxHeight: "320px",
          overflowY: "auto",
          background: "#1b1e24",
          border: "1px solid #2a2e36",
          borderRadius: "8px",
          padding: "10px",
          zIndex: 15,
          fontSize: "12px",
          boxShadow: "0 8px 24px rgba(0,0,0,.45)"
        }
      },
      [
        _("div", { style: { display: "flex", justifyContent: "space-between", marginBottom: "6px" } }, [
          _("b", { style: { color: "#c9cdd6" } }, "任务面板(todo)"),
          _("button", { onClick: () => void e.refresh(), style: Zn }, "↻ 刷新")
        ]),
        e.err ? _("div", { style: { color: "#ff7b72" } }, e.err) : e.todos.length === 0 ? _("div", { style: { color: "#7d838f" } }, "暂无任务(模型可经 todo 工具建单)") : e.todos.map(
          (s) => _(
            "div",
            {
              key: s.id,
              style: {
                display: "flex",
                gap: "8px",
                padding: "4px 0",
                borderBottom: "1px solid #2a2e36",
                color: "#c9cdd6"
              }
            },
            [
              _("span", { style: { color: Xn[s.status] ?? "#7d838f", fontWeight: 600 } }, Qn[s.status] ?? s.status),
              _("span", { style: { flex: 1 } }, s.subject),
              s.status === "in_progress" && s.activeForm ? _("span", { style: { color: "#6eb3ff" } }, s.activeForm) : null
            ]
          )
        )
      ]
    ) : null;
    return _("div", null, [n, r]);
  }
}, Zn = {
  background: "none",
  border: "1px solid #2a2e36",
  borderRadius: "4px",
  color: "#6eb3ff",
  cursor: "pointer",
  fontSize: "11px",
  padding: "1px 8px"
};
export {
  es as default
};
