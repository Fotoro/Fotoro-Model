#include <QApplication>
#include <QMainWindow>
#include <QWidget>
#include <QHBoxLayout>
#include <QVBoxLayout>
#include <QListView>
#include <QLineEdit>
#include <QSlider>
#include <QLabel>
#include <QPushButton>
#include <QAbstractListModel>
#include <QStyledItemDelegate>
#include <QPainter>
#include <QNetworkAccessManager>
#include <QNetworkRequest>
#include <QNetworkReply>
#include <QJsonDocument>
#include <QJsonArray>
#include <QJsonObject>
#include <QPixmap>
#include <QPainterPath>
#include <QScrollBar>
#include <QKeyEvent>

const QString BASE_URL = "http://127.0.0.1:8080";

// ═══════════════════════════════════════════════════════════════════
//  DATA MODEL & REPOSITORIES
// ═══════════════════════════════════════════════════════════════════

struct ApiImage {
    QString hash;
    QString caption;
    QString category;
    QString thumbnailPath;
    QPixmap thumbnailPixmap;
    bool isFetchingThumb = false;
};

class ImageModel : public QAbstractListModel {
    Q_OBJECT
public:
    QVector<ApiImage> images;

    int rowCount(const QModelIndex &parent = QModelIndex()) const override {
        return images.size();
    }

    QVariant data(const QModelIndex &index, int role = Qt::DisplayRole) const override {
        if (!index.isValid() || index.row() >= images.size()) return QVariant();
        if (role == Qt::UserRole) {
            return index.row();
        }
        return QVariant();
    }

    void clear() {
        beginResetModel();
        images.clear();
        endResetModel();
    }

    void appendImages(const QVector<ApiImage> &newImages) {
        if (newImages.isEmpty()) return;
        beginInsertRows(QModelIndex(), images.size(), images.size() + newImages.size() - 1);
        images.append(newImages);
        endInsertRows();
    }
};

// ═══════════════════════════════════════════════════════════════════
//  GRID ITEM DELEGATE (Renders Individual Cards)
// ═══════════════════════════════════════════════════════════════════

class ImageDelegate : public QStyledItemDelegate {
public:
    void paint(QPainter *painter, const QStyleOptionViewItem &option, const QModelIndex &index) const override {
        painter->save();
        painter->setRenderHint(QPainter::Antialiasing);

        const ImageModel* model = static_cast<const ImageModel*>(index.model());
        const ApiImage& img = model->images[index.row()];

        QRect rect = option.rect.adjusted(4, 4, -4, -4);
        bool isSelected = option.state & QStyle::State_Selected;

        // Draw Card Background
        QColor bg = isSelected ? QColor("#3c3420") : QColor("#1c1f26");
        painter->setPen(Qt::NoPen);
        painter->setBrush(bg);
        painter->drawRoundedRect(rect, 6, 6);

        // Draw Selection Border
        if (isSelected) {
            painter->setPen(QPen(QColor("#f5a623"), 2));
            painter->setBrush(Qt::NoBrush);
            painter->drawRoundedRect(rect.adjusted(-1, -1, 1, 1), 6, 6);
        }

        // Draw Image Thumbnail
        if (!img.thumbnailPixmap.isNull()) {
            QPixmap scaled = img.thumbnailPixmap.scaled(rect.size(), Qt::KeepAspectRatioByExpanding, Qt::SmoothTransformation);
            QRect targetRect = rect.adjusted(2, 2, -2, -2);
            
            QPainterPath path;
            path.addRoundedRect(targetRect, 4, 4);
            painter->setClipPath(path);
            
            // Center crop alignment
            int xOff = (scaled.width() - targetRect.width()) / 2;
            int yOff = (scaled.height() - targetRect.height()) / 2;
            painter->drawPixmap(targetRect.x(), targetRect.y(), targetRect.width(), targetRect.height(), 
                                scaled, xOff, yOff, targetRect.width(), targetRect.height());
        } else {
            // Skeleton Shimmer Box
            painter->setPen(Qt::NoPen);
            painter->setBrush(QColor("#252830"));
            painter->drawRoundedRect(rect.adjusted(2, 2, -2, -2), 4, 4);
        }

        painter->restore();
    }

    QSize sizeHint(const QStyleOptionViewItem &option, const QModelIndex &index) const override {
        Q_UNUSED(option);
        Q_UNUSED(index);
        return QSize(200, 200); // Handled dynamically via Main Window zoom
    }
};

// ═══════════════════════════════════════════════════════════════════
//  LIGHTBOX OVERLAY VIEW
// ═══════════════════════════════════════════════════════════════════

class LightboxOverlay : public QWidget {
    Q_OBJECT
public:
    QPixmap fullPixmap;
    QString captionText;
    bool isLoading = false;

    LightboxOverlay(QWidget *parent) : QWidget(parent) {
        setAttribute(Qt::WA_NoSystemBackground);
        setVisible(false);
    }

    void showImage(const QString& hash, const QString& caption) {
        captionText = caption;
        isLoading = true;
        fullPixmap = QPixmap();
        setVisible(true);
        update();
    }

    void setPixmap(const QPixmap& pixmap) {
        fullPixmap = pixmap;
        isLoading = false;
        update();
    }

protected:
    void paintEvent(QPaintEvent *) override {
        QPainter painter(this);
        // Dim backdrop
        painter.fillRect(rect(), QColor(8, 9, 12, 230));

        QRect area = rect().adjusted(60, 60, -60, -100);

        if (isLoading) {
            painter.setPen(QColor("#7b8096"));
            painter.drawText(rect(), Qt::AlignCenter, "Loading High-Res Asset...");
        } else if (!fullPixmap.isNull()) {
            QPixmap scaled = fullPixmap.scaled(area.size(), Qt::KeepAspectRatio, Qt::SmoothTransformation);
            QRect targetRect(rect().center().x() - scaled.width() / 2, 
                             rect().center().y() - scaled.height() / 2 - 20, 
                             scaled.width(), scaled.height());
            painter.drawPixmap(targetRect.x(), targetRect.y(), scaled);
            
            // Render Caption Bottom Bar
            QRect textRect(0, rect().bottom() - 60, rect().width(), 40);
            painter.setPen(QColor("#e8eaf0"));
            painter.drawText(textRect, Qt::AlignCenter, captionText);
        }
    }

    void mousePressEvent(QMouseEvent *) override {
        setVisible(false); // Close on overlay background tap
    }
};

// ═══════════════════════════════════════════════════════════════════
//  MAIN APPLICATION WINDOW
// ═══════════════════════════════════════════════════════════════════

class FotoroWindow : public QMainWindow {
    Q_OBJECT
private:
    ImageModel *model;
    QListView *gridView;
    QLineEdit *searchBar;
    QSlider *zoomSlider;
    QLabel *statusLabel;
    LightboxOverlay *lightbox;
    QNetworkAccessManager *networkManager;
    
    int currentPage = 1;
    bool isFetchingNextPage = false;
    QString activeCategory = "";
    QString activeQuery = "";

public:
    FotoroWindow() {
        QWidget *centralWidget = new QWidget(this);
        setCentralWidget(centralWidget);
        resize(1200, 800);

        networkManager = new QNetworkAccessManager(this);
        model = new ImageModel();

        // High-performance virtual grid layout setup
        gridView = new QListView(this);
        gridView->setViewMode(QListView::IconMode);
        gridView->setResizeMode(QListView::Adjust);
        gridView->setMovement(QListView::Static);
        gridView->setUniformItemSizes(true);
        gridView->setGridSize(QSize(200, 200));
        gridView->setModel(model);
        gridView->setItemDelegate(new ImageDelegate());

        // Sidebar Assembly
        QWidget *sidebar = new QWidget(this);
        sidebar->setObjectName("Sidebar");
        sidebar->setFixedWidth(220);
        QVBoxLayout *sidebarLayout = new QVBoxLayout(sidebar);
        sidebarLayout->setContentsMargins(0, 16, 0, 16);
        
        QLabel *libHeader = new QLabel("  LIBRARY", this);
        libHeader->setStyleSheet("color: #7b8096; font-weight: bold; font-size: 10px;");
        sidebarLayout->addWidget(libHeader);

        QStringList categories = {"All Images", "Photos", "Screenshots", "Documents", "Wallpapers", "People"};
        for (const QString& cat : categories) {
            QPushButton *btn = new QPushButton("  " + cat, this);
            btn->setCheckable(true);
            btn->setAutoExclusive(true);
            if (cat == "All Images") btn->setChecked(true);
            sidebarLayout->addWidget(btn);

            connect(btn, &QPushButton::clicked, [this, cat]() {
                activeCategory = (cat == "All Images") ? "" : cat.toLower();
                activeQuery = "";
                searchBar->clear();
                reloadGallery();
            });
        }
        sidebarLayout->addStretch();

        // Toolbar Assembly
        QWidget *toolbar = new QWidget(this);
        toolbar->setObjectName("Toolbar");
        QHBoxLayout *toolbarLayout = new QHBoxLayout(toolbar);
        
        QLabel *logo = new QLabel("fotoro", this);
        logo->setStyleSheet("color: #f5a623; font-weight: bold; font-size: 18px; padding-left: 8px;");
        
        searchBar = new QLineEdit(this);
        searchBar->setPlaceholderText("Search native layout library...");
        
        zoomSlider = new QSlider(Qt::Horizontal, this);
        zoomSlider->setRange(100, 320);
        zoomSlider->setValue(200);
        zoomSlider->setFixedWidth(120);

        toolbarLayout->addWidget(logo);
        toolbarLayout->addWidget(searchBar, 1);
        toolbarLayout->addWidget(zoomSlider);

        // Main Layout Structure
        QVBoxLayout *rightContentLayout = new QVBoxLayout();
        rightContentLayout->setContentsMargins(0,0,0,0);
        rightContentLayout->setSpacing(0);
        rightContentLayout->addWidget(toolbar);
        rightContentLayout->addWidget(gridView);

        // Status Footer
        statusLabel = new QLabel("System Ready", this);
        statusLabel->setStyleSheet("color: #7b8096; background-color: #13151a; padding: 4px 12px; font-size: 11px;");
        rightContentLayout->addWidget(statusLabel);

        QHBoxLayout *mainLayout = new QHBoxLayout(centralWidget);
        mainLayout->setContentsMargins(0,0,0,0);
        mainLayout->setSpacing(0);
        mainLayout->addWidget(sidebar);
        mainLayout->addLayout(rightContentLayout);

        // Lightbox layer initialization
        lightbox = new LightboxOverlay(centralWidget);

        // Signal Pipelines
        connect(searchBar, &QLineEdit::returnPressed, this, &FotoroWindow::executeSearch);
        connect(zoomSlider, &QSlider::valueChanged, this, &FotoroWindow::adjustGridSize);
        connect(gridView->verticalScrollBar(), &QScrollBar::valueChanged, this, &FotoroWindow::checkInfiniteScroll);
        connect(gridView, &QListView::clicked, this, &FotoroWindow::handleItemClicked);

        reloadGallery();
    }

protected:
    void resizeEvent(QResizeEvent *event) override {
        QMainWindow::resizeEvent(event);
        lightbox->setGeometry(centralWidget()->rect());
    }

    void keyPressEvent(QKeyEvent *event) override {
        if (event->key() == Qt::Key_Escape && lightbox->isVisible()) {
            lightbox->setVisible(false);
        } else {
            QMainWindow::keyPressEvent(event);
        }
    }

private:
    void reloadGallery() {
        currentPage = 1;
        model->clear();
        fetchPage(currentPage);
    }

    void fetchPage(int page) {
        if (isFetchingNextPage) return;
        isFetchingNextPage = true;
        statusLabel->setText("Querying assets backend pipeline...");

        QString urlStr = QString("%1/api/images?page=%2&per_page=50&sort=date_desc").arg(BASE_URL).arg(page);
        if (!activeCategory.isEmpty()) urlStr += "&category=" + activeCategory;

        QNetworkReply *reply = networkManager->get(QNetworkRequest(QUrl(urlStr)));
        connect(reply, &QNetworkReply::finished, [this, reply]() {
            isFetchingNextPage = false;
            if (reply->error() == QNetworkReply::NoError) {
                QJsonDocument doc = QJsonDocument::fromJson(reply->readAll());
                QJsonArray arr = doc.isArray() ? doc.array() : doc.object().value("results").toArray();
                
                QVector<ApiImage> freshImages;
                for (const auto& val : arr) {
                    QJsonObject obj = val.toObject();
                    ApiImage img;
                    img.hash = obj.value("hash").toString();
                    img.caption = obj.value("caption").toString();
                    img.category = obj.value("category").toString();
                    img.thumbnailPath = obj.value("thumbnail").toString();
                    freshImages.append(img);
                }
                model->appendImages(freshImages);
                statusLabel->setText(QString("Displaying %1 local index items").arg(model->images.size()));
                triggerVisibleThumbnailLoads();
            }
            reply->deleteLater();
        });
    }

    void executeSearch() {
        activeQuery = searchBar->text();
        if (activeQuery.isEmpty()) { reloadGallery(); return; }

        model->clear();
        statusLabel->setText("Running high-dimensional vector extraction match...");
        
        QString urlStr = QString("%1/api/search?q=%2").arg(BASE_URL).arg(QUrl::toPercentEncoding(activeQuery));
        QNetworkReply *reply = networkManager->get(QNetworkRequest(QUrl(urlStr)));
        connect(reply, &QNetworkReply::finished, [this, reply]() {
            if (reply->error() == QNetworkReply::NoError) {
                QJsonDocument doc = QJsonDocument::fromJson(reply->readAll());
                QJsonArray arr = doc.isArray() ? doc.array() : doc.object().value("results").toArray();
                
                QVector<ApiImage> searchResults;
                for (const auto& val : arr) {
                    QJsonObject obj = val.toObject();
                    ApiImage img;
                    img.hash = obj.value("hash").toString();
                    img.caption = obj.value("caption").toString();
                    img.thumbnailPath = obj.value("thumbnail").toString();
                    searchResults.append(img);
                }
                model->appendImages(searchResults);
                statusLabel->setText(QString("Found %1 vector matches").arg(model->images.size()));
                triggerVisibleThumbnailLoads();
            }
            reply->deleteLater();
        });
    }

    void triggerVisibleThumbnailLoads() {
        // Find visible indexes to request thumbnails efficiently (Lazy Evaluation)
        QRect viewportRect = gridView->viewport()->rect();
        QModelIndex startIdx = gridView->indexAt(viewportRect.topLeft());
        QModelIndex endIdx = gridView->indexAt(viewportRect.bottomRight());

        if (!startIdx.isValid()) return;
        int endRow = endIdx.isValid() ? endIdx.row() : model->images.size() - 1;

        // Add 10 items padding ahead for seamless scrolling injection
        endRow = qMin(endRow + 10, model->images.size() - 1);

        for (int i = startIdx.row(); i <= endRow; ++i) {
            if (model->images[i].thumbnailPixmap.isNull() && !model->images[i].isFetchingThumb) {
                model->images[i].isFetchingThumb = true;
                fetchThumbnail(i);
            }
        }
    }

    void fetchThumbnail(int row) {
        QString urlStr = BASE_URL + model->images[row].thumbnailPath;
        QNetworkReply *reply = networkManager->get(QNetworkRequest(QUrl(urlStr)));
        connect(reply, &QNetworkReply::finished, [this, reply, row]() {
            if (reply->error() == QNetworkReply::NoError) {
                QPixmap pix;
                if (pix.loadFromData(reply->readAll())) {
                    model->images[row].thumbnailPixmap = pix;
                    emit model->dataChanged(model->index(row), model->index(row));
                }
            }
            reply->deleteLater();
        });
    }

    void handleItemClicked(const QModelIndex &index) {
        if (!index.isValid()) return;
        const ApiImage& img = model->images[index.row()];
        
        lightbox->showImage(img.hash, img.caption);
        
        QString urlStr = QString("%1/api/image/%2").arg(BASE_URL).arg(img.hash);
        QNetworkReply *reply = networkManager->get(QNetworkRequest(QUrl(urlStr)));
        connect(reply, &QNetworkReply::finished, [this, reply]() {
            if (reply->error() == QNetworkReply::NoError) {
                QPixmap pix;
                if (pix.loadFromData(reply->readAll())) {
                    lightbox->setPixmap(pix);
                }
            }
            reply->deleteLater();
        });
    }

    void adjustGridSize(int value) {
        gridView->setGridSize(QSize(value, value));
        triggerVisibleThumbnailLoads();
    }

    void checkInfiniteScroll(int value) {
        triggerVisibleThumbnailLoads();
        if (activeQuery.isEmpty() && !isFetchingNextPage && value >= gridView->verticalScrollBar()->maximum() - 100) {
            currentPage++;
            fetchPage(currentPage);
        }
    }
};

// ═══════════════════════════════════════════════════════════════════
//  THEME ENGINE STYLESHEET (IDE Slate Dark / Amber Accent)
// ═══════════════════════════════════════════════════════════════════

const QString IDE_THEME_QSS = R"(
    QMainWindow { background-color: #13151a; }
    QWidget#Sidebar { background-color: #1c1f26; border-right: 1px solid #2e3240; }
    QWidget#Toolbar { background-color: #1c1f26; border-bottom: 1px solid #2e3240; min-height: 50px; }
    
    QListView { 
        background-color: #13151a; 
        border: none; 
        outline: none;
        padding-top: 10px;
        padding-left: 10px;
    }
    
    QLineEdit {
        background-color: #252830;
        border: 1px solid #2e3240;
        border-radius: 4px;
        color: #e8eaf0;
        padding: 5px 12px;
        font-size: 13px;
        margin: 8px 20px;
    }
    QLineEdit:focus { border: 1px solid #f5a623; }

    QPushButton {
        color: #b4b7c2;
        background-color: transparent;
        border: none;
        border-left: 3px solid transparent;
        text-align: left;
        padding: 10px 16px;
        font-size: 13px;
    }
    QPushButton:hover { background-color: #2e3240; color: #e8eaf0; }
    QPushButton:checked { 
        color: #f5a623; 
        background-color: rgba(245, 166, 35, 20); 
        border-left: 3px solid #f5a623; 
        font-weight: bold;
    }

    QScrollBar:vertical {
        background-color: #13151a;
        width: 8px;
        margin: 0px;
    }
    QScrollBar::handle:vertical {
        background-color: #2e3240;
        min-height: 20px;
        border-radius: 4px;
    }
    QScrollBar::handle:vertical:hover { background-color: #f5a623; }
    QScrollBar::add-line:vertical, QScrollBar::sub-line:vertical { border: none; background: none; }
    
    QSlider::groove:horizontal {
        height: 4px;
        background: #2e3240;
        border-radius: 2px;
    }
    QSlider::handle:horizontal {
        background: #e8eaf0;
        width: 12px;
        margin: -4px 0;
        border-radius: 6px;
    }
    QSlider::handle:horizontal:hover { background: #f5a623; }
)";

int main(int argc, char *argv[]) {
    QApplication app(argc, argv);
    app.setStyleSheet(IDE_THEME_QSS);
    
    FotoroWindow window;
    window.show();
    return app.exec();
}

#include "main.moc"
