# Request mirroring skill — oddiy tushuntirish

## Bu nima?

Sizda ishlab turgan servis bor (masalan, **Python v1**). Uning yangi versiyasini yozdingiz (**Python v2**, yoki Go, Node — farqi yo'q). Savol: yangisi eskisi bilan **bir xil ishlaydimi?**

Testlar hamma holatni qamramaydi. Eng ishonchli tekshiruv — **haqiqiy foydalanuvchilarning haqiqiy so'rovlari**. Lekin yangi versiyani to'g'ridan-to'g'ri prod'ga qo'yish xavfli.

Bu skill shu muammoni hal qiladi:

```
foydalanuvchi ─► eski servis (v1) ─► javob foydalanuvchiga ketadi ✅
                    │
                    └─ so'rov nusxasi ─► yangi servis (v2) ─► javob faqat solishtiriladi 🔍
```

- Foydalanuvchi **doim eski servis javobini** oladi. Hech narsa sezmaydi.
- Xuddi shu so'rov yangi servisga ham yuboriladi.
- Ikkala javob solishtiriladi va natija **Grafana**'da ko'rinadi.

## Misol

Haydovchi ilovada buyurtmalar ro'yxatini ochdi:

| | Eski servis (v1) | Yangi servis (v2) |
|---|---|---|
| Javob | 14 ta buyurtma | 13 ta buyurtma |
| Vaqt | 120 ms | 15 ms |
| Haydovchiga ketdimi? | ✅ ha | ❌ yo'q, faqat solishtirildi |

Grafana'da shunday yozuv paydo bo'ladi: *"`/orders` — farq bor, `$.results` uzunligi 14 != 13"*. Demak v2 da xato bor, uni cutover'dan **oldin** tuzatasiz.

## Yangi servis prod'ni buzib qo'ymaydimi?

Yo'q. Yangi servis alohida "qum qutisida" ishlaydi:

| Narsa | Eski servis | Yangi servis |
|---|---|---|
| Baza | haqiqiy baza | bazaning **nusxasi** |
| Redis / kesh | haqiqiy | alohida |
| Telegram, SMS, AI, to'lov | haqiqiy | **soxta** (`fakeapi`) — hech kimga xabar ketmaydi |

Yana bitta himoya bor: solishtiruvchi proksi to'xtab qolsa, nginx avtomatik ravishda to'g'ridan-to'g'ri eski servisga o'tadi.

## Grafana'da nimalar ko'rinadi

![demo](docs/demo-dashboard.png)

| Panel | Ma'nosi |
|---|---|
| **Parity %** | Javoblarning necha foizi bir xil chiqdi. O'tish uchun maqsad: ≥ 99.5% |
| **Differences** | Nechta javob farq qildi |
| **p95 v1 / p95 v2** | So'rovlarning 95 foizi qancha vaqtda javob oldi |
| **Per endpoint** | Har bir URL bo'yicha tezlik va moslik, eng yomoni tepada |
| **Status code differences** | Masalan: v1 `400` qaytardi, v2 esa `401` |
| **Differences jadvali** | Farq qilgan har bir so'rov: ikkala javob yonma-yon va aynan qaysi maydon farq qilgani |
| **RAM / CPU** | Qaysi versiya qancha resurs yeydi |

Natija turlari:

| Natija | Ma'nosi |
|---|---|
| `match` | Javoblar bir xil ✅ |
| `body_diff` | Status bir xil, lekin javob matni boshqa |
| `status_diff` | Status kodi boshqa (masalan 200 va 404) |
| `shadow_error` / `shadow_timeout` | Yangi servis javob bermadi yoki kechikdi |

## Claude bu skill'ni qanday ishlatadi

**1. O'rnatasiz (bir marta):**

```bash
git clone https://github.com/Abduqodir7007/request-mirroring.git ~/.claude/skills/request-mirroring
```

**2. Claude Code'ga oddiy qilib yozasiz:**

> orders-api servisini v1 dan v2 ga mirror qil va natijani Grafana'da ko'rsat

**3. Claude o'zi tushunadi.** Skill avtomatik yuklanadi. Uni `/request-mirroring` deb qo'lda chaqirish ham mumkin.

**4. Keyin Claude:**
1. Serverni faqat o'qib o'rganadi: nginx, docker, baza, tashqi API'lar.
2. Shablonlarni sizning servis nomlaringizga moslab yozadi.
3. Bazaning nusxasini oladi va yangi servisni soxta API'lar bilan ishga tushiradi.
4. Grafana dashboard'ini yasaydi.
5. **Har bir prod o'zgarishidan oldin sizdan ruxsat so'raydi.**
6. Mirror'ni yoqadi va farqlarni tahlil qiladi: bu haqiqiy xatomi yoki shunchaki eskirgan ma'lumotmi.

**Yangilash:** `git -C ~/.claude/skills/request-mirroring pull`

## O'zingiz 2 daqiqada sinab ko'ring

Kompyuteringizda faqat Docker bo'lsa yetadi:

```bash
cd example
docker compose up -d --build
```

Keyin brauzerda **http://127.0.0.1:3000** ni oching (login `admin`, parol `admin`) → **Dashboards → Mirror**.

Demo'da Python v1 va Python v2 solishtiriladi. v2 ga ataylab 3 ta farq qo'yilgan, Grafana ularning hammasini ko'rsatadi:
- ro'yxat teskari tartibda qaytadi;
- har 10-mahsulot narxi son o'rniga matn bo'lib qoladi;
- parol bo'sh bo'lsa `400` o'rniga `401` qaytadi.

O'chirish: `docker compose down -v`

## Qaysi tillar bilan ishlaydi?

**Hammasi bilan.** Proksi faqat HTTP'ni biladi. Shuning uchun quyidagilarning hammasi bo'ladi:

- Python → Python (yangi versiya)
- Python → Go, Node → Go, PHP → Python
- Django 4 → Django 5, gunicorn → uvicorn
- Katta monolitdan bitta qismni alohida servisga ajratish

Go'da faqat solishtiruvchi proksi yozilgan (`tool/` papkasi). U alohida Docker konteyner bo'lib ishlaydi, sizning servisingizga Go o'rnatish shart emas.

## Papkalar

| Papka | Ichida nima bor |
|---|---|
| `SKILL.md` | Claude uchun asosiy yo'riqnoma |
| `references/` | Batafsil qo'llanmalar: qanday ishlaydi, bosqichlar, baza, Grafana, oldin bo'lgan xatolar |
| `templates/` | Tayyor fayllar: docker-compose, nginx, yoqish/o'chirish skriptlari, Grafana |
| `tool/` | Go'dagi proksi kodi |
| `example/` | Ishlaydigan demo |

## Eng muhim 3 qoida

1. Foydalanuvchi **doim eski servis javobini** oladi.
2. Yangi servis **hech qachon** haqiqiy bazaga, Telegram'ga, SMS'ga yoki to'lovga tegmaydi.
3. Mirror bitta buyruq bilan yoqiladi va o'chiriladi: `mirror-switch on` / `mirror-switch off`.
