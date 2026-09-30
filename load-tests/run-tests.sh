#!/bin/bash

echo "🏦 Bank Prototype - Test Runner"
echo "================================"
echo ""

# Проверка доступности сервера
echo " Проверка доступности сервера..."
if ! curl -s http://localhost:8080/health > /dev/null; then
    echo " Сервер недоступен на http://localhost:8080"
    echo "Пожалуйста, запустите сервер командой: ./start-server.sh"
    exit 1
fi
echo " Сервер доступен на http://localhost:8080"
echo ""

# Меню выбора теста
echo " Выберите тест для запуска:"
echo "1) Smoke Test (1 минута - быстрая проверка)"
echo "2) Load Test (18 минут - нормальная нагрузка)"
echo "3) Stress Test (41 минута - стресс-тест)"
echo "4) Spike Test (3 минуты - пиковая нагрузка)"
echo "5) Full Scenario (12 минут - полный сценарий)"
echo "6) Запустить ВСЕ тесты последовательно (~75 минут)"
echo ""
read -p "Введите номер (1-6): " choice

case $choice in
    1)
        echo ""
        echo " Запуск Smoke Test..."
        k6 run scenarios/smoke-test.js
        ;;
    2)
        echo ""
        echo " Запуск Load Test..."
        k6 run scenarios/load-test.js
        ;;
    3)
        echo ""
        echo " Запуск Stress Test..."
        k6 run scenarios/stress-test.js
        ;;
    4)
        echo ""
        echo " Запуск Spike Test..."
        k6 run scenarios/spike-test.js
        ;;
    5)
        echo ""
        echo " Запуск Full Scenario..."
        k6 run scenarios/full-scenario.js
        ;;
    6)
        echo ""
        echo " Запуск ВСЕХ тестов..."
        echo ""

        echo " [1/5] Smoke Test..."
        k6 run scenarios/smoke-test.js

        echo ""
        echo " [2/5] Load Test..."
        k6 run scenarios/load-test.js

        echo ""
        echo " [3/5] Stress Test..."
        k6 run scenarios/stress-test.js

        echo ""
        echo " [4/5] Spike Test..."
        k6 run scenarios/spike-test.js

        echo ""
        echo " [5/5] Full Scenario..."
        k6 run scenarios/full-scenario.js
        ;;
    *)
        echo " Неверный выбор"
        exit 1
        ;;
esac

echo ""
echo "================================"
echo " Тестирование завершено!"
echo " Все результаты выведены выше"
echo "================================"

