#include <QApplication>
#include <QMainWindow>
#include <QWidget>
#include <QHBoxLayout>
#include <QVBoxLayout>
#include <QGridLayout>
#include <QScrollArea>
#include <QLineEdit>
#include <QSlider>
#include <QLabel>
#include <QPushButton>
#include <QPainter>
#include <QPainterPath>
#include <QNetworkAccessManager>
#include <QNetworkRequest>
#include <QNetworkReply>
#include <QJsonDocument>
#include <QJsonArray>
#include <QJsonObject>
#include <QPixmap>
#include <QScrollBar>
#include <QKeyEvent>
#include <QPropertyAnimation>
#include <QGraphicsOpacityEffect>
#include <QGraphicsDropShadowEffect>
#include <QTimer>
#include <QFrame>
#include <QResizeEvent>
#include <QMouseEvent>
#include <QEnterEvent>
#include <QParallelAnimationGroup>
#include <QSequentialAnimationGroup>
#include <QVariantAnimation>
#include <QEasingCurve>
#include <QPointer>
#include <cmath>

// ─── compile with: ───────────────────────────────────────────────
//  qmake CONFIG+=c++17  (add QT += network widgets in .pro)
//  or: g++ ... $(pkg-config --cflags --libs Qt5Widgets Qt5Network)
// ─────────────────────────────────────────────────────────────────

const QString BASE_URL = "http://127.0.0.1:8080";

// ═══════════════════════════════════════════════════════════════════
//  DATA MODEL
// ═══════════════════════════════════════════════════════════════════

struct ImageData {
    QString hash;
    QString caption;
    QString category;
    QString thumbnailPath;
    QPixmap thumbnailPixmap;
    double  score = 0.0;      // 0 → no score (browse mode)
    bool    thumbLoading = false;
};

// ═══════════════════════════════════════════════════════════════════
//  SHIMMER ANIMATION WIDGET  — used as placeholder while thumb loads
// ═══════════════════════════════════════════════════════════════════

class ShimmerWidget : public QWidget {
    Q_OBJECT
    Q_PROPERTY(qreal shimmerPos READ shimmerPos WRITE setShimmerPos)
public:
    ShimmerWidget(QWidget *parent = nullptr) : QWidget(parent) {
        setAttribute(Qt::WA_OpaquePaintEvent, false);
        _anim = new QVariantAnimation(this);
        _anim->setStartValue(0.0);
        _anim->setEndValue(1.0);
        _anim->setDuration(1400);
        _anim->setLoopCount(-1);
        _anim->setEasingCurve(QEasingCurve::Linear);
        connect(_anim, &QVariantAnimation::valueChanged, [this](const QVariant &v) {
            _shimmerPos = v.toReal();
            update();
        });
        _anim->start();
    }

    qreal shimmerPos() const { return _shimmerPos; }
    void  setShimmerPos(qreal v) { _shimmerPos = v; update(); }

    void stopShimmer() { _anim->stop(); }

protected:
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.setRenderHint(QPainter::Antialiasing);

        QPainterPath path;
        path.addRoundedRect(rect(), 10, 10);
        p.setClipPath(path);

        // dark base
        p.fillRect(rect(), QColor("#13151c"));

        // travelling highlight band
        qreal pos = _shimmerPos * (width() + 200) - 100;
        QLinearGradient grad(pos - 80, 0, pos + 80, height());
        grad.setColorAt(0.0, QColor(255,255,255,0));
        grad.setColorAt(0.5, QColor(255,255,255,14));
        grad.setColorAt(1.0, QColor(255,255,255,0));
        p.fillRect(rect(), grad);
    }

private:
    qreal              _shimmerPos = 0.0;
    QVariantAnimation *_anim;
};

// ═══════════════════════════════════════════════════════════════════
//  IMAGE CARD WIDGET  — the main grid tile with hover animation
// ═══════════════════════════════════════════════════════════════════

class ImageCard : public QFrame {
    Q_OBJECT
    Q_PROPERTY(qreal hoverProgress READ hoverProgress WRITE setHoverProgress)
public:
    ImageData data;

    ImageCard(QWidget *parent = nullptr) : QFrame(parent) {
        setObjectName("ImageCard");
        setAttribute(Qt::WA_Hover, true);
        setCursor(Qt::PointingHandCursor);
        setFixedSize(CARD_W, CARD_H);
        setMouseTracking(true);

        // Shimmer placeholder
        _shimmer = new ShimmerWidget(this);
        _shimmer->setGeometry(0, 0, CARD_W, CARD_H);
        _shimmer->show();

        // Hover animation
        _hoverAnim = new QPropertyAnimation(this, "hoverProgress", this);
        _hoverAnim->setDuration(180);
        _hoverAnim->setEasingCurve(QEasingCurve::OutCubic);

        // Fade-in animation for when thumb arrives
        _opacityEffect = new QGraphicsOpacityEffect(this);
        _opacityEffect->setOpacity(0.0);
        this->setGraphicsEffect(_opacityEffect);
        _fadeIn = new QPropertyAnimation(_opacityEffect, "opacity", this);
        _fadeIn->setDuration(280);
        _fadeIn->setEasingCurve(QEasingCurve::OutCubic);
        _fadeIn->setStartValue(0.85);
        _fadeIn->setEndValue(1.0);
    }

    static const int CARD_W = 200;
    static const int CARD_H = 150;

    void setThumbnail(const QPixmap &pix) {
        data.thumbnailPixmap = pix;
        if (_shimmer) {
            _shimmer->stopShimmer();
            _shimmer->hide();
        }
        _fadeIn->start();
        update();
    }

    void setCardData(const ImageData &d) {
        data = d;
        // Reset shimmer if new image
        if (data.thumbnailPixmap.isNull()) {
            if (_shimmer) _shimmer->show();
        }
        update();
    }

    qreal hoverProgress() const { return _hoverProgress; }
    void  setHoverProgress(qreal v) { _hoverProgress = v; update(); }

signals:
    void clicked(const ImageData &data);

protected:
    void enterEvent(QEnterEvent *) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hoverProgress);
        _hoverAnim->setEndValue(1.0);
        _hoverAnim->start();
    }

    void leaveEvent(QEvent *) override {
        _hoverAnim->stop();
        _hoverAnim->setStartValue(_hoverProgress);
        _hoverAnim->setEndValue(0.0);
        _hoverAnim->start();
    }

    void mousePressEvent(QMouseEvent *e) override {
        if (e->button() == Qt::LeftButton) {
            emit clicked(data);
        }
    }

    void paintEvent(QPaintEvent *) override {
        QPainter painter(this);
        painter.setRenderHints(QPainter::Antialiasing | QPainter::SmoothPixmapTransform);

        // ── Hover lift: scale up slightly from center ──
        if (_hoverProgress > 0.0) {
            qreal scale = 1.0 + _hoverProgress * 0.035;
            qreal tx = width()  * (1.0 - scale) / 2.0;
            qreal ty = height() * (1.0 - scale) / 2.0;
            painter.translate(tx, ty);
            painter.scale(scale, scale);
        }

        QRect r = rect();
        QPainterPath clip;
        clip.addRoundedRect(r, 10, 10);
        painter.setClipPath(clip);

        // ── Thumbnail or dark bg ──
        if (!data.thumbnailPixmap.isNull()) {
            QPixmap scaled = data.thumbnailPixmap.scaled(
                r.size(), Qt::KeepAspectRatioByExpanding, Qt::SmoothTransformation);
            int xOff = (scaled.width()  - r.width())  / 2;
            int yOff = (scaled.height() - r.height()) / 2;
            painter.drawPixmap(0, 0, r.width(), r.height(),
                               scaled, xOff, yOff, r.width(), r.height());
        } else {
            // Shimmer widget paints itself; just need a base color behind it
            painter.fillRect(r, QColor("#13151c"));
        }

        painter.setClipping(false);

        // ── Hover sheen overlay (subtle bright wave at top on hover) ──
        if (_hoverProgress > 0.0) {
            // Scale clip needs re-apply after clipping was cleared
            QPainterPath clip2;
            clip2.addRoundedRect(r, 10, 10);
            painter.setClipPath(clip2);

            QLinearGradient sheen(0, 0, 0, r.height() * 0.55);
            sheen.setColorAt(0.0, QColor(255,255,255, int(28 * _hoverProgress)));
            sheen.setColorAt(1.0, QColor(255,255,255, 0));
            painter.fillRect(r, sheen);

            // Border glow ring
            painter.setClipping(false);
            QPen borderPen(QColor(255, 255, 255, int(55 * _hoverProgress)), 1.5);
            painter.setPen(borderPen);
            painter.setBrush(Qt::NoBrush);
            painter.drawRoundedRect(QRectF(r).adjusted(0.75, 0.75, -0.75, -0.75), 10, 10);
        }

        // ── Bottom gradient scrim ──
        if (!data.thumbnailPixmap.isNull() || _hoverProgress > 0) {
            QPainterPath clip3;
            clip3.addRoundedRect(r, 10, 10);
            painter.setClipPath(clip3);

            QLinearGradient scrim(0, r.height() * 0.45, 0, r.height());
            scrim.setColorAt(0.0, QColor(0,0,0,0));
            scrim.setColorAt(1.0, QColor(0,0,0,185));
            painter.fillRect(r, scrim);
            painter.setClipping(false);
        }

        // ── Caption badge (bottom-left) ──
        QFont font = painter.font();
        font.setPointSizeF(8.5);
        painter.setFont(font);
        QFontMetrics fm(font);

        if (!data.caption.isEmpty()) {
            QString text = fm.elidedText(data.caption, Qt::ElideRight, r.width() - 56);
            int tw  = fm.horizontalAdvance(text) + 12;
            QRect tr(8, r.bottom() - 26, tw, 18);
            painter.setPen(Qt::NoPen);
            painter.setBrush(QColor(10, 12, 18, 215));
            painter.drawRoundedRect(tr, 5, 5);
            painter.setPen(QColor(210, 213, 220));
            painter.drawText(tr, Qt::AlignCenter, text);
        }

        // ── Score badge (bottom-right, only when > 0) ──
        if (data.score > 0.0) {
            QString scoreStr = QString::number(data.score, 'f', 2);
            int sw  = fm.horizontalAdvance(scoreStr) + 12;
            QRect sr(r.right() - 8 - sw, r.bottom() - 26, sw, 18);
            painter.setPen(Qt::NoPen);
            painter.setBrush(QColor(10, 12, 18, 215));
            painter.drawRoundedRect(sr, 5, 5);
            painter.setPen(Qt::white);
            painter.drawText(sr, Qt::AlignCenter, scoreStr);
        }
    }

private:
    ShimmerWidget            *_shimmer       = nullptr;
    QPropertyAnimation       *_hoverAnim     = nullptr;
    QGraphicsOpacityEffect   *_opacityEffect = nullptr;
    QPropertyAnimation       *_fadeIn        = nullptr;
    qreal                     _hoverProgress = 0.0;
};

// ═══════════════════════════════════════════════════════════════════
//  FLOW GRID  — responsive wrapping layout container
// ═══════════════════════════════════════════════════════════════════

class FlowGrid : public QWidget {
    Q_OBJECT
public:
    int  cardW       = ImageCard::CARD_W;
    int  cardH       = ImageCard::CARD_H;
    int  hSpacing    = 10;
    int  vSpacing    = 10;
    QVector<ImageCard*> cards;

    explicit FlowGrid(QWidget *parent = nullptr) : QWidget(parent) {
        setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Minimum);
    }

    void addCard(ImageCard *card) {
        card->setParent(this);
        cards.append(card);
        card->show();
        relayout();
    }

    void clearCards() {
        for (auto *c : cards) { c->hide(); c->deleteLater(); }
        cards.clear();
        setFixedHeight(0);
    }

    void relayout() {
        if (cards.isEmpty()) { setFixedHeight(0); return; }
        int w      = width();
        int cols   = qMax(1, (w + hSpacing) / (cardW + hSpacing));
        int rows   = (cards.size() + cols - 1) / cols;
        int totalH = rows * (cardH + vSpacing) - vSpacing + 16;

        // Centre the grid horizontally
        int gridW  = cols * (cardW + hSpacing) - hSpacing;
        int xStart = qMax(0, (w - gridW) / 2);

        for (int i = 0; i < cards.size(); ++i) {
            int col = i % cols;
            int row = i / cols;
            int x   = xStart + col * (cardW + hSpacing);
            int y   = 8 + row * (cardH + vSpacing);
            cards[i]->setFixedSize(cardW, cardH);
            cards[i]->move(x, y);
        }
        setFixedHeight(totalH);
    }

    void resizeCards(int w, int h) {
        cardW = w;
        cardH = h;
        for (auto *c : cards) c->setFixedSize(w, h);
        relayout();
    }

protected:
    void resizeEvent(QResizeEvent *) override { relayout(); }
};

// ═══════════════════════════════════════════════════════════════════
//  LIGHTBOX OVERLAY
// ═══════════════════════════════════════════════════════════════════

class LightboxOverlay : public QWidget {
    Q_OBJECT
public:
    explicit LightboxOverlay(QWidget *parent) : QWidget(parent) {
        setAttribute(Qt::WA_NoSystemBackground);
        setVisible(false);

        _fadeAnim = new QPropertyAnimation(this, "windowOpacity", this);
        _fadeAnim->setDuration(200);
        _fadeAnim->setEasingCurve(QEasingCurve::OutCubic);
    }

    void show(const QPixmap &pix, const QString &caption) {
        _pixmap  = pix;
        _caption = caption;
        setVisible(true);
        raise();
        _fadeAnim->stop();
        _fadeAnim->setStartValue(0.0);
        _fadeAnim->setEndValue(1.0);
        _fadeAnim->start();
        update();
    }

    void showLoading(const QString &caption) {
        _pixmap  = QPixmap();
        _caption = caption;
        _loading = true;
        setVisible(true);
        raise();
        update();
    }

    void setFullPixmap(const QPixmap &pix) {
        _pixmap  = pix;
        _loading = false;
        update();
    }

protected:
    void paintEvent(QPaintEvent *) override {
        QPainter p(this);
        p.fillRect(rect(), QColor(5, 6, 9, 235));

        if (_loading) {
            p.setPen(QColor("#525866"));
            p.drawText(rect(), Qt::AlignCenter, "Loading…");
            return;
        }
        if (_pixmap.isNull()) return;

        QRect area = rect().adjusted(80, 80, -80, -80);
        QPixmap scaled = _pixmap.scaled(area.size(), Qt::KeepAspectRatio, Qt::SmoothTransformation);
        int x = rect().center().x() - scaled.width()  / 2;
        int y = rect().center().y() - scaled.height() / 2 - 18;
        p.drawPixmap(x, y, scaled);

        p.setPen(QColor("#cdd0d8"));
        QFont f = p.font();
        f.setPointSize(11);
        p.setFont(f);
        QRect textR(0, rect().bottom() - 56, rect().width(), 36);
        p.drawText(textR, Qt::AlignCenter, _caption);

        // Close hint
        f.setPointSize(9);
        p.setFont(f);
        p.setPen(QColor("#525866"));
        p.drawText(QRect(0, rect().bottom() - 28, rect().width(), 20),
                   Qt::AlignCenter, "Press Esc or click to close");
    }

    void mousePressEvent(QMouseEvent *) override {
        _fadeAnim->stop();
        _fadeAnim->setStartValue(1.0);
        _fadeAnim->setEndValue(0.0);
        connect(_fadeAnim, &QPropertyAnimation::finished, this, [this]() {
            setVisible(false);
            disconnect(_fadeAnim, &QPropertyAnimation::finished, nullptr, nullptr);
        });
        _fadeAnim->start();
    }

private:
    QPixmap             _pixmap;
    QString             _caption;
    bool                _loading = false;
    QPropertyAnimation *_fadeAnim;
};

// ═══════════════════════════════════════════════════════════════════
//  SIDEBAR BUTTON
// ═══════════════════════════════════════════════════════════════════

class SidebarBtn : public QPushButton {
public:
    SidebarBtn(const QString &label, const QString &count, QWidget *parent = nullptr)
        : QPushButton(parent) {
        setCheckable(true);
        setAutoExclusive(true);
        setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Fixed);
        setFixedHeight(34);

        auto *row = new QHBoxLayout(this);
        row->setContentsMargins(14, 0, 14, 0);

        _titleLbl = new QLabel(label, this);
        _titleLbl->setStyleSheet("background: transparent; font-size: 13px;");

        _countLbl = new QLabel(count, this);
        _countLbl->setStyleSheet("background: transparent; font-size: 11px; color: #525866;");

        row->addWidget(_titleLbl);
        row->addStretch();
        row->addWidget(_countLbl);
    }

    void setCount(const QString &c) { _countLbl->setText(c); }

private:
    QLabel *_titleLbl;
    QLabel *_countLbl;
};

// ═══════════════════════════════════════════════════════════════════
//  MAIN WINDOW
// ═══════════════════════════════════════════════════════════════════

class FotoroWindow : public QMainWindow {
    Q_OBJECT
public:
    FotoroWindow() {
        setWindowTitle("fotoro");
        resize(1300, 840);
        setMinimumSize(800, 600);

        _net = new QNetworkAccessManager(this);

        // ── Central widget ──
        auto *central = new QWidget(this);
        setCentralWidget(central);

        // ── Top bar ──
        auto *topBar = new QWidget(central);
        topBar->setObjectName("TopBar");
        topBar->setFixedHeight(48);
        auto *topLayout = new QHBoxLayout(topBar);
        topLayout->setContentsMargins(18, 0, 18, 0);

        auto *dotsRow = new QHBoxLayout();
        dotsRow->setSpacing(8);
        for (int i = 0; i < 3; ++i) {
            auto *dot = new QFrame(topBar);
            dot->setFixedSize(12, 12);
            QStringList dotColors = {"#ff5f57","#ffbd2e","#28c841"};
            dot->setStyleSheet(QString("background:%1;border-radius:6px;").arg(dotColors[i]));
            dotsRow->addWidget(dot);
        }
        topLayout->addLayout(dotsRow);
        topLayout->addStretch();

        _serverBadge = new QLabel("● fotoro.local · indexed 24,381 items", topBar);
        _serverBadge->setObjectName("ServerBadge");
        topLayout->addWidget(_serverBadge);
        topLayout->addStretch();
        topLayout->addSpacing(60); // balance spacer

        // ── Sidebar ──
        auto *sidebar = new QWidget(central);
        sidebar->setObjectName("Sidebar");
        sidebar->setFixedWidth(234);
        auto *sideLayout = new QVBoxLayout(sidebar);
        sideLayout->setContentsMargins(8, 20, 8, 20);
        sideLayout->setSpacing(2);

        struct CatDef { QString label; QString count; QString key; };
        QVector<CatDef> cats = {
            {"Library","24,381",""},
            {"Favorites","412","favorites"},
            {"People","38","people"},
            {"Places","61","places"},
            {"Events","172","events"}
        };
        bool first = true;
        for (const auto &c : cats) {
            auto *btn = new SidebarBtn(c.label, c.count, sidebar);
            if (first) { btn->setChecked(true); first = false; }
            sideLayout->addWidget(btn);
            connect(btn, &QPushButton::clicked, [this, c]() {
                _activeCategory = c.key;
                _activeQuery    = "";
                _searchBar->clear();
                reloadGallery();
            });
        }

        auto *devHdr = new QLabel("DEVICES", sidebar);
        devHdr->setStyleSheet("color:#525866;font-size:10px;font-weight:bold;"
                              "padding:18px 14px 6px 14px;background:transparent;");
        sideLayout->addWidget(devHdr);

        struct DevDef { QString name; QString stat; };
        QVector<DevDef> devs = {
            {"Pixel 8 Pro","Synced 2m ago"},
            {"MacBook Pro","Hosting"},
            {"iPad Air","Indexing"}
        };
        for (const auto &d : devs) {
            auto *btn = new SidebarBtn(d.name, d.stat, sidebar);
            sideLayout->addWidget(btn);
        }
        sideLayout->addStretch();

        // ── Search bar ──
        _searchBar = new QLineEdit(central);
        _searchBar->setObjectName("SearchBar");
        _searchBar->setPlaceholderText("Search your library…");
        _searchBar->setFixedHeight(42);

        auto *searchRow = new QHBoxLayout(_searchBar);
        searchRow->setContentsMargins(0, 0, 12, 0);
        searchRow->addStretch();
        auto *kbd = new QLabel("⌘K", _searchBar);
        kbd->setStyleSheet("color:#525866;background:#0d0e15;border:1px solid #1a1d26;"
                           "border-radius:4px;padding:2px 7px;font-size:10px;");
        searchRow->addWidget(kbd);

        // ── Gallery scroll area ──
        _scrollArea = new QScrollArea(central);
        _scrollArea->setObjectName("Gallery");
        _scrollArea->setWidgetResizable(true);
        _scrollArea->setHorizontalScrollBarPolicy(Qt::ScrollBarAlwaysOff);
        _scrollArea->setVerticalScrollBarPolicy(Qt::ScrollBarAsNeeded);

        _flowGrid = new FlowGrid();
        _scrollArea->setWidget(_flowGrid);

        // ── Bottom bar ──
        auto *bottomBar = new QWidget(central);
        bottomBar->setFixedHeight(38);
        auto *bottomLayout = new QHBoxLayout(bottomBar);
        bottomLayout->setContentsMargins(16, 0, 16, 0);

        _statusLabel = new QLabel("Ready", bottomBar);
        _statusLabel->setStyleSheet("color:#525866;font-size:12px;");

        _zoomSlider = new QSlider(Qt::Horizontal, bottomBar);
        _zoomSlider->setRange(120, 340);
        _zoomSlider->setValue(200);
        _zoomSlider->setFixedWidth(110);

        bottomLayout->addWidget(_statusLabel);
        bottomLayout->addStretch();
        bottomLayout->addWidget(new QLabel("Size:", bottomBar));
        bottomLayout->addWidget(_zoomSlider);

        // ── Right panel ──
        auto *rightPanel = new QVBoxLayout();
        rightPanel->setContentsMargins(14, 10, 14, 8);
        rightPanel->setSpacing(10);
        rightPanel->addWidget(_searchBar);
        rightPanel->addWidget(_scrollArea, 1);
        rightPanel->addWidget(bottomBar);

        // ── Content row ──
        auto *contentRow = new QHBoxLayout();
        contentRow->setContentsMargins(0,0,0,0);
        contentRow->setSpacing(0);
        contentRow->addWidget(sidebar);
        contentRow->addLayout(rightPanel);

        // ── Root layout ──
        auto *rootLayout = new QVBoxLayout(central);
        rootLayout->setContentsMargins(0,0,0,0);
        rootLayout->setSpacing(0);
        rootLayout->addWidget(topBar);
        rootLayout->addLayout(contentRow);

        // ── Lightbox ──
        _lightbox = new LightboxOverlay(central);

        // ── Connections ──
        connect(_searchBar, &QLineEdit::returnPressed, this, &FotoroWindow::executeSearch);
        connect(_zoomSlider, &QSlider::valueChanged,   this, &FotoroWindow::onZoom);
        connect(_scrollArea->verticalScrollBar(), &QScrollBar::valueChanged,
                this, &FotoroWindow::checkInfiniteScroll);

        // Debounce timer for re-layout after window resize
        _relayoutTimer = new QTimer(this);
        _relayoutTimer->setSingleShot(true);
        _relayoutTimer->setInterval(60);
        connect(_relayoutTimer, &QTimer::timeout, [this]() { _flowGrid->relayout(); });

        reloadGallery();
    }

protected:
    void resizeEvent(QResizeEvent *e) override {
        QMainWindow::resizeEvent(e);
        _lightbox->setGeometry(centralWidget()->rect());
        _relayoutTimer->start();
    }

    void keyPressEvent(QKeyEvent *e) override {
        if (e->key() == Qt::Key_Escape && _lightbox->isVisible()) {
            _lightbox->hide();
        } else {
            QMainWindow::keyPressEvent(e);
        }
    }

private slots:
    void onZoom(int v) {
        // Maintain a 4:3 ratio for cards
        int h = v;
        int w = int(v * 1.38);
        _flowGrid->resizeCards(w, h);
    }

    void checkInfiniteScroll(int val) {
        auto *sb = _scrollArea->verticalScrollBar();
        bool nearBottom  = (val >= sb->maximum() - 200);
        bool fitsInView  = (sb->maximum() == 0);

        if (_activeQuery.isEmpty() && !_fetching && (nearBottom || fitsInView)) {
            // Only paginate if last page was full
            if (_totalFetched > 0 && _totalFetched % 50 == 0) {
                _page++;
                fetchPage(_page);
            }
        }
    }

    void reloadGallery() {
        _page         = 1;
        _totalFetched = 0;
        _flowGrid->clearCards();
        fetchPage(_page);
    }

    void fetchPage(int page) {
        if (_fetching) return;
        _fetching = true;
        _statusLabel->setText("Loading…");

        QString url = QString("%1/api/images?page=%2&per_page=50&sort=date_desc")
                          .arg(BASE_URL).arg(page);
        if (!_activeCategory.isEmpty())
            url += "&category=" + _activeCategory;

        auto *reply = _net->get(QNetworkRequest(QUrl(url)));
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            _fetching = false;
            if (reply->error() != QNetworkReply::NoError) {
                _statusLabel->setText("Could not reach server — is fotoro running?");
                reply->deleteLater();
                return;
            }
            auto doc = QJsonDocument::fromJson(reply->readAll());
            auto arr = doc.isArray() ? doc.array()
                                     : doc.object().value("results").toArray();

            for (const auto &val : arr) {
                auto obj = val.toObject();
                ImageData d;
                d.hash          = obj.value("hash").toString();
                d.caption       = obj.value("caption").toString();
                d.category      = obj.value("category").toString();
                d.thumbnailPath = obj.value("thumbnail").toString();
                d.score         = 0.0; // browse mode — no score
                addCard(d);
            }
            _totalFetched += arr.size();
            _statusLabel->setText(QString("%1 items").arg(_totalFetched));
            reply->deleteLater();

            // If content doesn't fill the viewport, check again
            QTimer::singleShot(120, this, [this]() {
                checkInfiniteScroll(_scrollArea->verticalScrollBar()->value());
            });
        });
    }

    void executeSearch() {
        _activeQuery = _searchBar->text().trimmed();
        if (_activeQuery.isEmpty()) { reloadGallery(); return; }

        _page         = 1;
        _totalFetched = 0;
        _flowGrid->clearCards();
        _statusLabel->setText("Searching…");

        QString url = QString("%1/api/search?q=%2")
                          .arg(BASE_URL)
                          .arg(QString::fromUtf8(QUrl::toPercentEncoding(_activeQuery)));

        auto *reply = _net->get(QNetworkRequest(QUrl(url)));
        connect(reply, &QNetworkReply::finished, this, [this, reply]() {
            if (reply->error() != QNetworkReply::NoError) {
                _statusLabel->setText("Search failed — check server connection");
                reply->deleteLater();
                return;
            }
            auto doc = QJsonDocument::fromJson(reply->readAll());
            auto arr = doc.isArray() ? doc.array()
                                     : doc.object().value("results").toArray();

            for (const auto &val : arr) {
                auto obj = val.toObject();
                ImageData d;
                d.hash          = obj.value("hash").toString();
                d.caption       = obj.value("caption").toString();
                d.thumbnailPath = obj.value("thumbnail").toString();
                d.score         = obj.value("score").toDouble(); // real API score only
                addCard(d);
            }
            _totalFetched = arr.size();
            _statusLabel->setText(
                _totalFetched == 0
                    ? "No results found"
                    : QString("%1 results").arg(_totalFetched));
            reply->deleteLater();
        });
    }

    void addCard(const ImageData &d) {
        auto *card = new ImageCard(_flowGrid);
        card->setCardData(d);
        _flowGrid->addCard(card);

        connect(card, &ImageCard::clicked, this, &FotoroWindow::onCardClicked);

        // Staggered fade-in entrance: each card appears 18ms after the previous
        int idx = _flowGrid->cards.size() - 1;
        card->setGraphicsEffect(nullptr); // remove the opacity effect temporarily for entrance
        auto *eff = new QGraphicsOpacityEffect(card);
        eff->setOpacity(0.0);
        card->setGraphicsEffect(eff);
        auto *anim = new QPropertyAnimation(eff, "opacity", card);
        anim->setStartValue(0.0);
        anim->setEndValue(1.0);
        anim->setDuration(220);
        anim->setEasingCurve(QEasingCurve::OutCubic);
        QTimer::singleShot(qMin(idx * 18, 400), anim, [anim]() { anim->start(QAbstractAnimation::DeleteWhenStopped); });

        // Lazy thumbnail fetch
        if (!d.thumbnailPath.isEmpty()) {
            fetchThumbnail(card, d.thumbnailPath);
        }
    }

    void fetchThumbnail(ImageCard *card, const QString &path) {
        // Guard: card might be destroyed if gallery is cleared mid-fetch
        QPointer<ImageCard> safeCard = card;

        QString urlStr = BASE_URL + path;
        auto *reply = _net->get(QNetworkRequest(QUrl(urlStr)));
        connect(reply, &QNetworkReply::finished, this, [this, reply, safeCard]() {
            if (!safeCard) { reply->deleteLater(); return; }
            if (reply->error() == QNetworkReply::NoError) {
                QPixmap pix;
                if (pix.loadFromData(reply->readAll())) {
                    safeCard->setThumbnail(pix);
                }
            }
            reply->deleteLater();
        });
    }

    void onCardClicked(const ImageData &d) {
        _lightbox->showLoading(d.caption);
        _lightbox->setGeometry(centralWidget()->rect());
        _lightbox->raise();

        // Fetch full-res image
        QString urlStr = QString("%1/api/image/%2").arg(BASE_URL).arg(d.hash);
        auto *reply = _net->get(QNetworkRequest(QUrl(urlStr)));
        connect(reply, &QNetworkReply::finished, this, [this, reply, d]() {
            if (reply->error() == QNetworkReply::NoError) {
                QPixmap pix;
                if (pix.loadFromData(reply->readAll())) {
                    _lightbox->show(pix, d.caption);
                }
            }
            reply->deleteLater();
        });
    }

private:
    QNetworkAccessManager *_net            = nullptr;
    FlowGrid              *_flowGrid       = nullptr;
    QScrollArea           *_scrollArea     = nullptr;
    QLineEdit             *_searchBar      = nullptr;
    QSlider               *_zoomSlider     = nullptr;
    QLabel                *_statusLabel    = nullptr;
    QLabel                *_serverBadge   = nullptr;
    LightboxOverlay       *_lightbox       = nullptr;
    QTimer                *_relayoutTimer  = nullptr;

    QString _activeCategory;
    QString _activeQuery;
    int     _page         = 1;
    int     _totalFetched = 0;
    bool    _fetching     = false;
};

// ═══════════════════════════════════════════════════════════════════
//  STYLESHEET
// ═══════════════════════════════════════════════════════════════════

static const char *QSS = R"(
QMainWindow, QWidget {
    background-color: #090a0f;
    color: #c8ccd6;
    font-family: -apple-system, "SF Pro Display", "Segoe UI", "Helvetica Neue", Arial, sans-serif;
}

QWidget#TopBar {
    background-color: #090a0f;
    border-bottom: 1px solid #13151e;
    min-height: 48px;
}

QWidget#Sidebar {
    background-color: #0b0c12;
    border-right: 1px solid #13151e;
}

QLabel#ServerBadge {
    background-color: #0d0e15;
    border: 1px solid #1a1d28;
    border-radius: 12px;
    color: #6c7080;
    padding: 4px 16px;
    font-size: 11px;
    font-family: "SF Mono", "JetBrains Mono", monospace;
}

QLineEdit#SearchBar {
    background-color: #0e1018;
    border: 1px solid #1c1f2c;
    border-radius: 9px;
    color: #e8eaf0;
    padding: 0px 14px;
    font-size: 13px;
    selection-background-color: #2a3560;
}
QLineEdit#SearchBar:focus {
    border-color: #2a3060;
    background-color: #0f1120;
}

SidebarBtn {
    color: #8890a0;
    background-color: transparent;
    border: none;
    border-radius: 6px;
    text-align: left;
}
SidebarBtn:hover {
    background-color: #13151e;
    color: #e0e3ee;
}
SidebarBtn:checked {
    background-color: #161828;
    color: #e8eaff;
    font-weight: 600;
}

QScrollArea#Gallery {
    background-color: #090a0f;
    border: none;
}
QScrollArea#Gallery > QWidget > QWidget {
    background-color: #090a0f;
}

QScrollBar:vertical {
    background: #090a0f;
    width: 7px;
    margin: 0;
}
QScrollBar::handle:vertical {
    background: #1c2030;
    min-height: 28px;
    border-radius: 3px;
}
QScrollBar::handle:vertical:hover { background: #2c3050; }
QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical {
    border: none; background: none; height: 0;
}

QSlider::groove:horizontal {
    height: 3px;
    background: #1c2030;
    border-radius: 1.5px;
}
QSlider::handle:horizontal {
    background: #8890a0;
    width: 11px;
    height: 11px;
    margin: -4px 0;
    border-radius: 5.5px;
}
QSlider::handle:horizontal:hover { background: #e0e3ee; }
QSlider::sub-page:horizontal {
    background: #3040a0;
    border-radius: 1.5px;
}
)";

// ═══════════════════════════════════════════════════════════════════
//  ENTRY POINT
// ═══════════════════════════════════════════════════════════════════

int main(int argc, char *argv[]) {
    QApplication app(argc, argv);
    app.setStyleSheet(QSS);

    FotoroWindow w;
    w.show();
    return app.exec();
}

#include "main.moc"
